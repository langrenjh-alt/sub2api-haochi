// Package resourcepressure samples local process and host resource pressure.
// It performs no network I/O and treats every source independently so callers
// can continue operating when a platform does not expose a particular metric.
package resourcepressure

import (
	"context"
	"os"
	"runtime"
	"runtime/metrics"
	"time"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

const defaultGoMemoryLimit uint64 = 1<<63 - 1

// Ratio is a normalized pressure value in the range [0, 1].
type Ratio struct {
	Value float64
	Valid bool
}

// CgroupMemory is the memory usage and finite limit of the current cgroup.
// LimitValid is false for an unlimited cgroup.
type CgroupMemory struct {
	Version      int
	CurrentBytes uint64
	CurrentValid bool
	LimitBytes   uint64
	LimitValid   bool
	Pressure     Ratio
}

// ProcessMemory contains memory owned by this process.
type ProcessMemory struct {
	RSSBytes uint64
	Valid    bool
}

// GoMemory contains the memory visible to the Go runtime. RuntimeManagedBytes
// matches the quantity governed by GOMEMLIMIT (MemStats.Sys - HeapReleased).
type GoMemory struct {
	HeapAllocBytes        uint64
	HeapInuseBytes        uint64
	RuntimeManagedBytes   uint64
	SysBytes              uint64
	MemoryLimitBytes      uint64
	Valid                 bool
	MemoryLimitValid      bool
	MemoryLimitConfigured bool
	Pressure              Ratio
}

// HostMemory contains physical host memory availability.
type HostMemory struct {
	TotalBytes     uint64
	AvailableBytes uint64
	Valid          bool
	Pressure       Ratio
}

// FileDescriptorUsage contains process and system-wide Linux FD utilization.
// ProcessLimit is the process soft RLIMIT_NOFILE value. SystemOpen is derived
// from Linux file-nr as allocated minus unused handles.
type FileDescriptorUsage struct {
	ProcessOpen     uint64
	ProcessLimit    uint64
	ProcessValid    bool
	ProcessPressure Ratio

	SystemAllocated uint64
	SystemUnused    uint64
	SystemOpen      uint64
	SystemLimit     uint64
	SystemValid     bool
	SystemPressure  Ratio

	Pressure Ratio
}

// Snapshot is one best-effort, entirely local resource sample.
type Snapshot struct {
	SampledAt time.Time

	Cgroup  CgroupMemory
	Process ProcessMemory
	Go      GoMemory
	Host    HostMemory
	FD      FileDescriptorUsage

	MemoryPressure Ratio
	FDPressure     Ratio
	Pressure       Ratio
}

// Dependencies makes the sampler deterministic in tests and allows callers to
// replace OS collectors. Nil collectors are treated as unavailable.
type Dependencies struct {
	GOOS string
	Now  func() time.Time

	ReadFile        func(string) ([]byte, error)
	ProcessRSS      func(context.Context) (uint64, error)
	HostMemory      func(context.Context) (totalBytes, availableBytes uint64, err error)
	RuntimeMemory   func() GoMemory
	FileDescriptors func() FileDescriptorUsage
}

// Sampler collects resource pressure from local operating-system and runtime
// sources. It is safe for concurrent use when its injected dependencies are.
type Sampler struct {
	deps Dependencies
}

// NewSampler constructs a sampler backed by local OS and Go runtime metrics.
func NewSampler() *Sampler {
	return NewSamplerWithDependencies(defaultDependencies())
}

// NewSamplerWithDependencies constructs an injectable sampler. Empty GOOS and
// Now fields default to runtime.GOOS and time.Now; other nil sources remain
// unavailable.
func NewSamplerWithDependencies(deps Dependencies) *Sampler {
	if deps.GOOS == "" {
		deps.GOOS = runtime.GOOS
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Sampler{deps: deps}
}

// Sample returns all metrics that are available. Failure of one source does
// not invalidate other fields in the snapshot.
func (s *Sampler) Sample(ctx context.Context) Snapshot {
	if ctx == nil {
		ctx = context.Background()
	}

	out := Snapshot{SampledAt: s.deps.Now().UTC()}
	if s.deps.GOOS == "linux" && s.deps.ReadFile != nil {
		out.Cgroup = sampleCgroupMemory(s.deps.ReadFile)
	}
	if s.deps.ProcessRSS != nil {
		if rss, err := s.deps.ProcessRSS(ctx); err == nil {
			out.Process = ProcessMemory{RSSBytes: rss, Valid: true}
		}
	}
	if s.deps.RuntimeMemory != nil {
		out.Go = finalizeGoMemory(s.deps.RuntimeMemory())
	}
	if s.deps.HostMemory != nil {
		if total, available, err := s.deps.HostMemory(ctx); err == nil && total > 0 {
			if available > total {
				available = total
			}
			out.Host = HostMemory{
				TotalBytes:     total,
				AvailableBytes: available,
				Valid:          true,
				Pressure:       ratio(total-available, total),
			}
		}
	}
	if s.deps.FileDescriptors != nil {
		out.FD = finalizeFileDescriptors(s.deps.FileDescriptors())
	}

	out.MemoryPressure = maxRatio(out.Cgroup.Pressure, out.Go.Pressure, out.Host.Pressure)
	out.FDPressure = out.FD.Pressure
	out.Pressure = maxRatio(out.MemoryPressure, out.FDPressure)
	return out
}

func defaultDependencies() Dependencies {
	return Dependencies{
		GOOS:            runtime.GOOS,
		Now:             time.Now,
		ReadFile:        os.ReadFile,
		ProcessRSS:      defaultProcessRSS,
		HostMemory:      defaultHostMemory,
		RuntimeMemory:   defaultRuntimeMemory,
		FileDescriptors: defaultFileDescriptorUsage,
	}
}

func defaultProcessRSS(ctx context.Context) (uint64, error) {
	p, err := process.NewProcessWithContext(ctx, int32(os.Getpid()))
	if err != nil {
		return 0, err
	}
	info, err := p.MemoryInfoWithContext(ctx)
	if err != nil {
		return 0, err
	}
	return info.RSS, nil
}

func defaultHostMemory(ctx context.Context) (uint64, uint64, error) {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return 0, 0, err
	}
	return vm.Total, vm.Available, nil
}

func defaultRuntimeMemory() GoMemory {
	samples := []metrics.Sample{
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/memory/classes/heap/unused:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
		{Name: "/memory/classes/total:bytes"},
		{Name: "/gc/gomemlimit:bytes"},
	}
	metrics.Read(samples)

	heapObjects, heapObjectsOK := uint64Metric(samples[0])
	heapUnused, heapUnusedOK := uint64Metric(samples[1])
	heapReleased, heapReleasedOK := uint64Metric(samples[2])
	total, totalOK := uint64Metric(samples[3])
	memoryLimit, memoryLimitOK := uint64Metric(samples[4])

	out := GoMemory{}
	if heapObjectsOK && heapUnusedOK && heapObjects <= ^uint64(0)-heapUnused && totalOK && heapReleasedOK {
		out.HeapAllocBytes = heapObjects
		out.HeapInuseBytes = heapObjects + heapUnused
		out.SysBytes = total
		if heapReleased <= total {
			out.RuntimeManagedBytes = total - heapReleased
			out.Valid = true
		}
	}
	if memoryLimitOK {
		out.MemoryLimitBytes = memoryLimit
		out.MemoryLimitValid = out.MemoryLimitBytes > 0
		out.MemoryLimitConfigured = out.MemoryLimitValid && out.MemoryLimitBytes < defaultGoMemoryLimit
	}
	return out
}

func uint64Metric(sample metrics.Sample) (uint64, bool) {
	if sample.Value.Kind() != metrics.KindUint64 {
		return 0, false
	}
	return sample.Value.Uint64(), true
}

func finalizeGoMemory(in GoMemory) GoMemory {
	if in.Valid && in.MemoryLimitValid && in.MemoryLimitConfigured && in.MemoryLimitBytes > 0 {
		in.Pressure = ratio(in.RuntimeManagedBytes, in.MemoryLimitBytes)
	} else {
		in.Pressure = Ratio{}
	}
	return in
}

func finalizeFileDescriptors(in FileDescriptorUsage) FileDescriptorUsage {
	if in.ProcessValid && in.ProcessLimit > 0 {
		in.ProcessPressure = ratio(in.ProcessOpen, in.ProcessLimit)
	} else {
		in.ProcessPressure = Ratio{}
	}

	if in.SystemAllocated >= in.SystemUnused {
		in.SystemOpen = in.SystemAllocated - in.SystemUnused
	} else {
		in.SystemOpen = 0
		in.SystemValid = false
	}
	if in.SystemValid && in.SystemLimit > 0 {
		in.SystemPressure = ratio(in.SystemOpen, in.SystemLimit)
	} else {
		in.SystemPressure = Ratio{}
	}
	in.Pressure = maxRatio(in.ProcessPressure, in.SystemPressure)
	return in
}

func ratio(current, limit uint64) Ratio {
	if limit == 0 {
		return Ratio{}
	}
	value := float64(current) / float64(limit)
	if value > 1 {
		value = 1
	}
	return Ratio{Value: value, Valid: true}
}

func maxRatio(values ...Ratio) Ratio {
	var out Ratio
	for _, value := range values {
		if !value.Valid {
			continue
		}
		if !out.Valid || value.Value > out.Value {
			out = value
		}
	}
	return out
}

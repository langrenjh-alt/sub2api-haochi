package resourcepressure

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSamplerCombinesLocalPressureSources(t *testing.T) {
	sampledAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.FixedZone("test", 8*60*60))
	files := map[string]string{
		procSelfCgroup:    `0::/workload.slice/sub2api.service`,
		procSelfMountInfo: `29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw`,
		"/sys/fs/cgroup/workload.slice/sub2api.service/memory.current": "800",
		"/sys/fs/cgroup/workload.slice/sub2api.service/memory.max":     "1000",
	}

	sampler := NewSamplerWithDependencies(Dependencies{
		GOOS: "linux",
		Now:  func() time.Time { return sampledAt },
		ReadFile: func(name string) ([]byte, error) {
			value, ok := files[name]
			if !ok {
				return nil, errors.New("not found")
			}
			return []byte(value), nil
		},
		ProcessRSS: func(context.Context) (uint64, error) { return 600, nil },
		HostMemory: func(context.Context) (uint64, uint64, error) { return 2000, 500, nil },
		RuntimeMemory: func() GoMemory {
			return GoMemory{
				HeapAllocBytes:        500,
				HeapInuseBytes:        600,
				RuntimeManagedBytes:   700,
				SysBytes:              900,
				MemoryLimitBytes:      1000,
				Valid:                 true,
				MemoryLimitValid:      true,
				MemoryLimitConfigured: true,
			}
		},
		FileDescriptors: func() FileDescriptorUsage {
			return FileDescriptorUsage{
				ProcessOpen:     90,
				ProcessLimit:    100,
				ProcessValid:    true,
				SystemAllocated: 70,
				SystemUnused:    20,
				SystemLimit:     100,
				SystemValid:     true,
			}
		},
	})

	snapshot := sampler.Sample(context.Background())

	require.Equal(t, sampledAt.UTC(), snapshot.SampledAt)
	require.Equal(t, CgroupMemory{
		Version:      2,
		CurrentBytes: 800,
		CurrentValid: true,
		LimitBytes:   1000,
		LimitValid:   true,
		Pressure:     Ratio{Value: 0.8, Valid: true},
	}, snapshot.Cgroup)
	require.Equal(t, ProcessMemory{RSSBytes: 600, Valid: true}, snapshot.Process)
	require.Equal(t, Ratio{Value: 0.7, Valid: true}, snapshot.Go.Pressure)
	require.Equal(t, Ratio{Value: 0.75, Valid: true}, snapshot.Host.Pressure)
	require.Equal(t, uint64(50), snapshot.FD.SystemOpen)
	require.Equal(t, Ratio{Value: 0.9, Valid: true}, snapshot.FDPressure)
	require.Equal(t, Ratio{Value: 0.8, Valid: true}, snapshot.MemoryPressure)
	require.Equal(t, Ratio{Value: 0.9, Valid: true}, snapshot.Pressure)
}

func TestSamplerCgroupV1FiniteAndUnlimitedLimits(t *testing.T) {
	tests := []struct {
		name           string
		limit          string
		wantLimit      uint64
		wantLimitValid bool
		wantPressure   Ratio
	}{
		{name: "finite", limit: "1000", wantLimit: 1000, wantLimitValid: true, wantPressure: Ratio{Value: 0.4, Valid: true}},
		{name: "kernel unlimited sentinel", limit: "9223372036854771712"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{
				procSelfCgroup:    `5:memory:/tenant/sub2api`,
				procSelfMountInfo: `31 24 0:28 /tenant /sys/fs/cgroup/memory rw,nosuid,nodev,noexec,relatime - cgroup cgroup rw,memory`,
				"/sys/fs/cgroup/memory/sub2api/memory.usage_in_bytes": "400",
				"/sys/fs/cgroup/memory/sub2api/memory.limit_in_bytes": tt.limit,
			}
			sampler := NewSamplerWithDependencies(Dependencies{
				GOOS: "linux",
				ReadFile: func(name string) ([]byte, error) {
					value, ok := files[name]
					if !ok {
						return nil, errors.New("not found")
					}
					return []byte(value), nil
				},
			})

			snapshot := sampler.Sample(context.Background())

			require.Equal(t, 1, snapshot.Cgroup.Version)
			require.Equal(t, uint64(400), snapshot.Cgroup.CurrentBytes)
			require.True(t, snapshot.Cgroup.CurrentValid)
			require.Equal(t, tt.wantLimit, snapshot.Cgroup.LimitBytes)
			require.Equal(t, tt.wantLimitValid, snapshot.Cgroup.LimitValid)
			require.Equal(t, tt.wantPressure, snapshot.Cgroup.Pressure)
		})
	}
}

func TestSamplerUnavailableSourcesDegradeGracefully(t *testing.T) {
	sampler := NewSamplerWithDependencies(Dependencies{
		GOOS: "windows",
		ProcessRSS: func(context.Context) (uint64, error) {
			return 0, errors.New("unsupported")
		},
		HostMemory: func(context.Context) (uint64, uint64, error) {
			return 0, 0, errors.New("unsupported")
		},
	})

	snapshot := sampler.Sample(nil)

	require.False(t, snapshot.Cgroup.CurrentValid)
	require.False(t, snapshot.Process.Valid)
	require.False(t, snapshot.Go.Valid)
	require.False(t, snapshot.Host.Valid)
	require.False(t, snapshot.FD.ProcessValid)
	require.False(t, snapshot.FD.SystemValid)
	require.False(t, snapshot.MemoryPressure.Valid)
	require.False(t, snapshot.FDPressure.Valid)
	require.False(t, snapshot.Pressure.Valid)
}

func TestSamplerClampsRatiosAndHostAvailability(t *testing.T) {
	sampler := NewSamplerWithDependencies(Dependencies{
		GOOS: "windows",
		HostMemory: func(context.Context) (uint64, uint64, error) {
			return 100, 150, nil
		},
		FileDescriptors: func() FileDescriptorUsage {
			return FileDescriptorUsage{ProcessOpen: 150, ProcessLimit: 100, ProcessValid: true}
		},
	})

	snapshot := sampler.Sample(context.Background())

	require.Equal(t, uint64(100), snapshot.Host.AvailableBytes)
	require.Equal(t, Ratio{Value: 0, Valid: true}, snapshot.Host.Pressure)
	require.Equal(t, Ratio{Value: 1, Valid: true}, snapshot.FD.ProcessPressure)
	require.Equal(t, Ratio{Value: 1, Valid: true}, snapshot.Pressure)
}

func TestDefaultRuntimeMemoryReportsGOMEMLIMIT(t *testing.T) {
	sample := defaultRuntimeMemory()

	require.True(t, sample.Valid)
	require.Greater(t, sample.SysBytes, uint64(0))
	require.Greater(t, sample.MemoryLimitBytes, uint64(0))
	require.True(t, sample.MemoryLimitValid)
}

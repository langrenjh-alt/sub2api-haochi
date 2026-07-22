//go:build linux

package resourcepressure

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func defaultFileDescriptorUsage() FileDescriptorUsage {
	var out FileDescriptorUsage

	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err == nil && limit.Cur > 0 && limit.Cur != ^uint64(0) {
			out.ProcessOpen = uint64(len(entries))
			out.ProcessLimit = limit.Cur
			out.ProcessValid = true
		}
	}

	if raw, err := os.ReadFile("/proc/sys/fs/file-nr"); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) >= 3 {
			allocated, errAllocated := strconv.ParseUint(fields[0], 10, 64)
			unused, errUnused := strconv.ParseUint(fields[1], 10, 64)
			limit, errLimit := strconv.ParseUint(fields[2], 10, 64)
			if errAllocated == nil && errUnused == nil && errLimit == nil && limit > 0 && allocated >= unused {
				out.SystemAllocated = allocated
				out.SystemUnused = unused
				out.SystemLimit = limit
				out.SystemValid = true
			}
		}
	}

	return out
}

package resourcepressure

import (
	"path"
	"strconv"
	"strings"
)

const (
	procSelfCgroup    = "/proc/self/cgroup"
	procSelfMountInfo = "/proc/self/mountinfo"
	cgroupRoot        = "/sys/fs/cgroup"
)

type cgroupMembership struct {
	v2Path  string
	v2Valid bool
	v1Path  string
	v1Valid bool
}

type cgroupMount struct {
	root       string
	mountPoint string
}

func sampleCgroupMemory(readFile func(string) ([]byte, error)) CgroupMemory {
	membership := cgroupMembership{}
	if raw, err := readFile(procSelfCgroup); err == nil {
		membership = parseCgroupMembership(raw)
	}

	var v2Mounts, v1MemoryMounts []cgroupMount
	if raw, err := readFile(procSelfMountInfo); err == nil {
		v2Mounts, v1MemoryMounts = parseCgroupMounts(raw)
	}

	v2Dirs := make([]string, 0, len(v2Mounts)+2)
	if membership.v2Valid {
		for _, mount := range v2Mounts {
			if dir, ok := resolveCgroupDir(mount, membership.v2Path); ok {
				v2Dirs = append(v2Dirs, dir)
			}
		}
		v2Dirs = append(v2Dirs, path.Join(cgroupRoot, membership.v2Path))
	}
	v2Dirs = append(v2Dirs, cgroupRoot)
	for _, dir := range uniqueStrings(v2Dirs) {
		if current, ok := readUintFile(readFile, path.Join(dir, "memory.current")); ok {
			out := CgroupMemory{Version: 2, CurrentBytes: current, CurrentValid: true}
			if raw, err := readFile(path.Join(dir, "memory.max")); err == nil {
				value := strings.TrimSpace(string(raw))
				if value != "" && value != "max" {
					if limit, err := strconv.ParseUint(value, 10, 64); err == nil && limit > 0 {
						out.LimitBytes = limit
						out.LimitValid = true
						out.Pressure = ratio(current, limit)
					}
				}
			}
			return out
		}
	}

	v1Dirs := make([]string, 0, len(v1MemoryMounts)+2)
	if membership.v1Valid {
		for _, mount := range v1MemoryMounts {
			if dir, ok := resolveCgroupDir(mount, membership.v1Path); ok {
				v1Dirs = append(v1Dirs, dir)
			}
		}
		v1Dirs = append(v1Dirs, path.Join(cgroupRoot, "memory", membership.v1Path))
	}
	v1Dirs = append(v1Dirs, path.Join(cgroupRoot, "memory"))
	for _, dir := range uniqueStrings(v1Dirs) {
		if current, ok := readUintFile(readFile, path.Join(dir, "memory.usage_in_bytes")); ok {
			out := CgroupMemory{Version: 1, CurrentBytes: current, CurrentValid: true}
			if limit, ok := readUintFile(readFile, path.Join(dir, "memory.limit_in_bytes")); ok && limit > 0 && limit < 1<<60 {
				out.LimitBytes = limit
				out.LimitValid = true
				out.Pressure = ratio(current, limit)
			}
			return out
		}
	}

	return CgroupMemory{}
}

func parseCgroupMembership(raw []byte) cgroupMembership {
	var out cgroupMembership
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		groupPath := cleanCgroupPath(parts[2])
		if parts[0] == "0" && parts[1] == "" {
			out.v2Path = groupPath
			out.v2Valid = true
			continue
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller == "memory" {
				out.v1Path = groupPath
				out.v1Valid = true
				break
			}
		}
	}
	return out
}

func parseCgroupMounts(raw []byte) (v2 []cgroupMount, v1Memory []cgroupMount) {
	for _, line := range strings.Split(string(raw), "\n") {
		sections := strings.SplitN(strings.TrimSpace(line), " - ", 2)
		if len(sections) != 2 {
			continue
		}
		before := strings.Fields(sections[0])
		after := strings.Fields(sections[1])
		if len(before) < 5 || len(after) < 3 {
			continue
		}
		mount := cgroupMount{
			root:       unescapeMountInfo(before[3]),
			mountPoint: unescapeMountInfo(before[4]),
		}
		switch after[0] {
		case "cgroup2":
			v2 = append(v2, mount)
		case "cgroup":
			if commaListContains(after[2], "memory") {
				v1Memory = append(v1Memory, mount)
			}
		}
	}
	return v2, v1Memory
}

func resolveCgroupDir(mount cgroupMount, groupPath string) (string, bool) {
	mountRoot := cleanCgroupPath(mount.root)
	groupPath = cleanCgroupPath(groupPath)
	if !pathWithin(groupPath, mountRoot) {
		return "", false
	}
	relative := strings.TrimPrefix(groupPath, mountRoot)
	return path.Join(mount.mountPoint, relative), true
}

func pathWithin(candidate, parent string) bool {
	if parent == "/" {
		return strings.HasPrefix(candidate, "/")
	}
	return candidate == parent || strings.HasPrefix(candidate, parent+"/")
}

func cleanCgroupPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(value, "/"))
}

func readUintFile(readFile func(string) ([]byte, error), name string) (uint64, bool) {
	raw, err := readFile(name)
	if err != nil {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return value, err == nil
}

func commaListContains(list, want string) bool {
	for _, value := range strings.Split(list, ",") {
		if value == want {
			return true
		}
	}
	return false
}

func unescapeMountInfo(value string) string {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(value)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

//go:build !linux

package resourcepressure

func defaultFileDescriptorUsage() FileDescriptorUsage {
	return FileDescriptorUsage{}
}

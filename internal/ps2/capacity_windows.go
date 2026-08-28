//go:build windows

package ps2

import "os"

func filesystemCapacity(path string) (total, free int64, readOnly bool, filesystem string, err error) {
	probe, err := os.CreateTemp(path, ".ps3mgr-write-check-")
	if err != nil {
		return 0, 1 << 62, true, "unknown", nil
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return 0, 1 << 62, false, "unknown", nil
}

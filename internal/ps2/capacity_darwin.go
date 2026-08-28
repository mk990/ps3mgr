//go:build darwin

package ps2

import "syscall"

func filesystemCapacity(path string) (total, free int64, readOnly bool, filesystem string, err error) {
	var stat syscall.Statfs_t
	if err = syscall.Statfs(path, &stat); err != nil {
		return
	}
	total = int64(stat.Blocks) * int64(stat.Bsize)
	free = int64(stat.Bavail) * int64(stat.Bsize)
	readOnly = stat.Flags&1 != 0
	if syscall.Access(path, 2) != nil {
		readOnly = true
	}
	var name []byte
	for _, value := range stat.Fstypename {
		if value == 0 {
			break
		}
		name = append(name, byte(value))
	}
	filesystem = string(name)
	return
}

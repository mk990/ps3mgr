//go:build linux

package ps2

import (
	"fmt"
	"syscall"
)

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
	switch uint64(stat.Type) {
	case 0x4d44:
		filesystem = "vfat"
	case 0x2011BAB0:
		filesystem = "exfat"
	case 0xEF53:
		filesystem = "ext4"
	case 0x58465342:
		filesystem = "xfs"
	case 0x9123683E:
		filesystem = "btrfs"
	case 0x01021994:
		filesystem = "tmpfs"
	case 0x65735546:
		filesystem = "fuse"
	case 0x794c7630:
		filesystem = "overlay"
	case 0x6969:
		filesystem = "nfs"
	default:
		filesystem = fmt.Sprintf("unknown (0x%x)", uint64(stat.Type))
	}
	return
}

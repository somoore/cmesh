//go:build !windows

package probe

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

func diskInfo(path string) (map[string]any, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(abs, &st); err != nil {
		return nil, err
	}
	blockSize := uint64(st.Bsize)
	total := uint64(st.Blocks) * blockSize
	free := uint64(st.Bavail) * blockSize
	used := total - free
	return map[string]any{
		"path":        abs,
		"total_bytes": total,
		"free_bytes":  free,
		"used_bytes":  used,
	}, nil
}

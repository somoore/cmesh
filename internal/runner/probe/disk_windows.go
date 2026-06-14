//go:build windows

package probe

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func diskInfo(path string) (map[string]any, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	ptr, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return nil, err
	}
	var freeAvail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &freeAvail, &total, &totalFree); err != nil {
		return nil, err
	}
	used := total - totalFree
	return map[string]any{
		"path":        abs,
		"total_bytes": total,
		"free_bytes":  totalFree,
		"used_bytes":  used,
	}, nil
}

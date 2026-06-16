//go:build !windows

package service

func currentProcessElevated() (bool, error) {
	return true, nil
}

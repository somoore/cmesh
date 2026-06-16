//go:build windows

package service

import "golang.org/x/sys/windows"

func currentProcessElevated() (bool, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false, err
	}
	defer token.Close()
	return token.IsElevated(), nil
}

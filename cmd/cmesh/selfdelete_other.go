//go:build !windows

package main

import (
	"errors"
	"os"
)

func removeCurrentExecutable(exe string) (string, error) {
	if err := os.Remove(exe); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "removed " + exe, nil
}

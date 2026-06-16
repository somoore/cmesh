//go:build !windows

package main

import "fmt"

func runElevated(args ...string) error {
	return fmt.Errorf("elevation helper is not supported on this platform")
}

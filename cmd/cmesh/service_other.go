//go:build !windows

package main

import "fmt"

func runServiceDaemon(_ string) error {
	return fmt.Errorf("Windows service entrypoint is not supported on this platform")
}

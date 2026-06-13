//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cfgpkg "cmesh/internal/config"

	"golang.org/x/sys/windows/svc"
)

func runServiceDaemon(configPath string) error {
	return svc.Run("cmesh", &windowsService{configPath: configPath})
}

type windowsService struct {
	configPath string
}

func (s *windowsService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("service panic: %v", r)
			}
		}()
		errCh <- runDaemon(ctx, s.configPath, true)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepts}
	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				changes <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case err := <-errCh:
					if err != nil {
						return false, 1
					}
					return false, 0
				case <-time.After(20 * time.Second):
					return false, 1
				}
			default:
				changes <- svc.Status{State: svc.Running, Accepts: accepts}
			}
		case err := <-errCh:
			if err != nil {
				writeWindowsServiceError(s.configPath, err)
				return false, 1
			}
			return false, 0
		}
	}
}

func writeWindowsServiceError(configPath string, err error) {
	logPath := filepath.Join(os.TempDir(), "cmesh-service.log")
	if cfg, loadErr := cfgpkg.Load(configPath); loadErr == nil && cfg.Node.DataDir != "" {
		logPath = filepath.Join(cfg.Node.DataDir, "service.log")
	}
	if mkErr := os.MkdirAll(filepath.Dir(logPath), 0o700); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if openErr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %v\n", time.Now().Format(time.RFC3339), err)
}

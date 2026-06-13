//go:build windows

package main

import (
	"context"
	"time"

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
				return false, 1
			}
			return false, 0
		}
	}
}

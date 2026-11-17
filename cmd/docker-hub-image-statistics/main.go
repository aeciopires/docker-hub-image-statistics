// Command docker-hub-image-statistics collects and serves pull statistics of
// public Docker Hub images.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/aeciopires/docker-hub-image-statistics/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// SIGHUP reloads the configuration (file + environment) without exiting.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	reload := make(chan struct{}, 1)
	go func() {
		for range hup {
			select {
			case reload <- struct{}{}:
			default: // a reload is already pending
			}
		}
	}()

	code := app.Main(ctx, app.Env{
		Args:   os.Args[1:],
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Reload: reload,
	})
	signal.Stop(hup)
	stop()
	os.Exit(code)
}

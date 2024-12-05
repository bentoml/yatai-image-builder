package main

import (
	"log/slog"
	"os"
	"os/signal"

	"golang.org/x/sys/unix"

	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/logger"
	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/seekabletarfs"
)

func main() {
	if len(os.Args) < 3 {
		println("Usage: seekabletarfs <tar file> <mount point>")
		os.Exit(1)
	}
	tarPath := os.Args[1]
	mountPoint := os.Args[2]

	const (
		logLevelDebug = "debug"
		logLevelInfo  = "info"
	)

	currentLogLevelString := logLevelInfo

	rotateLogLevel := func() {
		if currentLogLevelString == logLevelDebug {
			currentLogLevelString = logLevelInfo
			logger.SetLevel(slog.LevelInfo)
		} else {
			currentLogLevelString = logLevelDebug
			logger.SetLevel(slog.LevelDebug)
		}
		logger.L().Info("Log level set to", slog.String("level", currentLogLevelString))
	}

	// watch usr1 signal to rotate log level
	go func() {
		shutdownSigCh := make(chan os.Signal, 1)
		usr1SigCh := make(chan os.Signal, 1)

		signal.Notify(shutdownSigCh, unix.SIGINT, unix.SIGTERM)
		signal.Notify(usr1SigCh, unix.SIGUSR1)

		for {
			select {
			case <-shutdownSigCh:
				os.Exit(0)
			case <-usr1SigCh:
				rotateLogLevel()
			}
		}
	}()

	err := seekabletarfs.Mount(tarPath, mountPoint)
	if err != nil {
		println(err.Error())
		os.Exit(1)
	}
}

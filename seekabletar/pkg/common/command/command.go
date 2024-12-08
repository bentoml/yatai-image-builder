package command

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/briandowns/spinner"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/logger"
)

var GlobalCommandOption = struct {
	Debug bool
	Quiet bool
}{}

type ICommandOption interface {
	Complete(ctx context.Context, args []string, argsLenAtDash int) error
	Validate(ctx context.Context) error
	Run(ctx context.Context, args []string) error
}

func MakeRunE(opt ICommandOption) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if GlobalCommandOption.Debug {
			logger.SetLevel(slog.LevelDebug)
		} else {
			logger.SetLevel(slog.LevelInfo)
		}

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

		argsLenAtDash := cmd.ArgsLenAtDash()

		ctx, cancel := context.WithCancel(cmd.Context())

		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)

		go func() {
			<-c
			fmt.Println("Stopping...")
			cancel()
		}()

		err := opt.Complete(ctx, args, argsLenAtDash)
		if err != nil {
			err = errors.Wrap(err, "failed to complete")
			return err
		}
		err = opt.Validate(ctx)
		if err != nil {
			err = errors.Wrap(err, "failed to validate")
			return err
		}
		return errors.Wrap(opt.Run(ctx, args), "failed to run")
	}
}

type SpinnerWrapper struct {
	spinner *spinner.Spinner
}

func StartSpinner(format string, args ...interface{}) *SpinnerWrapper {
	if !strings.HasPrefix(format, " ") {
		format = " " + format
	}

	if GlobalCommandOption.Quiet {
		return &SpinnerWrapper{}
	}

	s := spinner.New(spinner.CharSets[11], 100*time.Millisecond, spinner.WithWriter(os.Stderr))
	s.Suffix = fmt.Sprintf(format, args...)
	s.Start()
	return &SpinnerWrapper{
		spinner: s,
	}
}

func (s *SpinnerWrapper) Stop() {
	if s.spinner == nil {
		return
	}
	s.spinner.Stop()
	s.spinner = nil
}

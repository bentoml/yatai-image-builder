package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/bentoml/yatai-image-builder/pkg/common/blob"
	"github.com/bentoml/yatai-image-builder/pkg/common/envspec"
)

type Options struct {
	BaseImage      string
	PythonVersion  string
	Commands       []string
	PythonPackages []string

	LowerDir string
	DestDir  string
}

func (options *Options) Validate() error {
	if options.BaseImage == "" {
		return errors.New("base-image is required")
	}
	return nil
}

func (options *Options) String() string {
	return fmt.Sprintf("BaseImage: %s, PythonVersion: %s, Commands: %v, PythonPackages: %v", options.BaseImage, options.PythonVersion, options.Commands, options.PythonPackages)
}

func (options *Options) ToEnvironmentSpec() envspec.EnvironmentSpec {
	return envspec.EnvironmentSpec{
		BaseImage:      options.BaseImage,
		PythonVersion:  options.PythonVersion,
		Commands:       options.Commands,
		PythonPackages: options.PythonPackages,
	}
}

func run(ctx context.Context, options Options) error {
	if err := options.Validate(); err != nil {
		slog.ErrorContext(ctx, "Invalid options", "error", err)
		return err
	}

	blobStorage, err := blob.NewBlobStorage()
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create blob storage", "error", err)
	}

	builder := NewImageMounter(blobStorage)
	return builder.Mount(ctx, options.ToEnvironmentSpec(), options.LowerDir, options.DestDir)
}

func main() {
	options := Options{}
	rootCmd := &cobra.Command{
		Use:   "bento-image-mounter",
		Short: "Bento Image Mounter",
		Run: func(cmd *cobra.Command, args []string) {
			ctx := cmd.Context()
			err := run(ctx, options)
			if err != nil {
				slog.ErrorContext(ctx, "Failed to execute command", "error", err)
				os.Exit(1)
			}
		},
	}
	rootCmd.Flags().StringVarP(&options.BaseImage, "base-image", "b", "", "Base image")
	rootCmd.Flags().StringVarP(&options.PythonVersion, "python-version", "p", "", "Python version")
	rootCmd.Flags().StringSliceVarP(&options.Commands, "commands", "c", []string{}, "Commands")
	rootCmd.Flags().StringSliceVarP(&options.PythonPackages, "python-packages", "P", []string{}, "Python packages")
	rootCmd.Flags().StringVarP(&options.LowerDir, "lower-dir", "l", "", "Lower directory")
	rootCmd.Flags().StringVarP(&options.DestDir, "dest-dir", "d", "", "Destination directory")
	err := rootCmd.Execute()
	if err != nil {
		slog.ErrorContext(context.Background(), "Failed to execute command", "error", err)
		os.Exit(1)
	}
}

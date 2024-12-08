package cmd

import (
	"context"
	"io"
	"os"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/common/command"
	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/seekabletar"
)

type ConvertOption struct {
	TarFilePath string
	DestPath    string
}

func (opt *ConvertOption) Complete(ctx context.Context, args []string, argsLenAtDash int) error {
	return nil
}

func (opt *ConvertOption) Validate(ctx context.Context) error {
	if opt.TarFilePath == "" {
		return errors.New("tar file path is required")
	}
	if opt.DestPath == "" {
		return errors.New("dest path is required")
	}
	return nil
}

func (opt *ConvertOption) Run(ctx context.Context, args []string) error {
	file, err := os.Open(opt.TarFilePath)
	if err != nil {
		return errors.Wrap(err, "failed to open tar file")
	}
	defer file.Close()

	seekableTar, err := seekabletar.ConvertToSeekableTar(file)
	if err != nil {
		return errors.Wrap(err, "failed to convert to seekable tar")
	}

	file, err = os.Create(opt.DestPath)
	if err != nil {
		return errors.Wrap(err, "failed to create dest file")
	}
	defer file.Close()

	_, err = io.Copy(file, seekableTar)
	if err != nil {
		return errors.Wrap(err, "failed to copy")
	}
	return nil
}

func NewConvertCommand() *cobra.Command {
	opt := &ConvertOption{}
	cmd := &cobra.Command{
		Use:   "convert",
		Short: "Convert a tar file to a seekable tar file",
		RunE:  command.MakeRunE(opt),
	}
	cmd.Flags().StringVarP(&opt.TarFilePath, "tar-file-path", "t", "", "Path to the tar file")
	cmd.Flags().StringVarP(&opt.DestPath, "dest-path", "d", "", "Path to the dest file")
	return cmd
}

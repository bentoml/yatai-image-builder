package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/common/command"
	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/seekabletar"
)

type TOCOption struct {
	TarFilePath string
}

func (opt *TOCOption) Complete(ctx context.Context, args []string, argsLenAtDash int) error {
	return nil
}

func (opt *TOCOption) Validate(ctx context.Context) error {
	if opt.TarFilePath == "" {
		return errors.New("tar file path is required")
	}
	return nil
}

func (opt *TOCOption) Run(ctx context.Context, args []string) error {
	file, err := os.Open(opt.TarFilePath)
	if err != nil {
		return errors.Wrap(err, "failed to open tar file")
	}
	defer file.Close()

	toc, err := seekabletar.GetTOCFromSeekableTar(file)
	if err != nil {
		return errors.Wrap(err, "failed to get toc")
	}

	b, err := json.MarshalIndent(toc, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to marshal toc")
	}

	fmt.Println(string(b))
	return nil
}

func NewTOCCommand() *cobra.Command {
	opt := &TOCOption{}
	cmd := &cobra.Command{
		Use:   "toc",
		Short: "Print the table of contents of a seekable tar file",
		RunE:  command.MakeRunE(opt),
	}
	cmd.Flags().StringVarP(&opt.TarFilePath, "tar-file-path", "t", "", "Path to the tar file")
	return cmd
}

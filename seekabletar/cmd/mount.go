package cmd

import (
	"context"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/common/command"
	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/seekabletarfs"
)

type MountOption struct {
	TarFilePath string
	MountPoint  string
}

func (opt *MountOption) Complete(ctx context.Context, args []string, argsLenAtDash int) error {
	return nil
}

func (opt *MountOption) Validate(ctx context.Context) error {
	if opt.TarFilePath == "" {
		return errors.New("tar file path is required")
	}
	if opt.MountPoint == "" {
		return errors.New("mount point is required")
	}
	return nil
}

func (opt *MountOption) Run(ctx context.Context, args []string) error {
	err := seekabletarfs.Mount(opt.TarFilePath, opt.MountPoint)
	if err != nil {
		return errors.Wrap(err, "failed to mount")
	}
	return nil
}

func NewMountCommand() *cobra.Command {
	opt := &MountOption{}
	cmd := &cobra.Command{
		Use:   "mount",
		Short: "Mount a seekable tar file to a directory",
		RunE:  command.MakeRunE(opt),
	}
	cmd.Flags().StringVarP(&opt.TarFilePath, "tar-file-path", "t", "", "Path to the tar file")
	cmd.Flags().StringVarP(&opt.MountPoint, "mount-point", "m", "", "Path to the mount point")
	return cmd
}

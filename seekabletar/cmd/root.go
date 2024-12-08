package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/common/command"
)

var rootCmd = &cobra.Command{
	Use:   "seekabletar",
	Short: "seekabletar",
	Long:  `seekabletar`,
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&command.GlobalCommandOption.Debug, "debug", "d", false, "debug mode, output verbose output")
	rootCmd.PersistentFlags().BoolVarP(&command.GlobalCommandOption.Quiet, "quiet", "q", false, "disable spinner and logs")
	rootCmd.AddCommand(NewTOCCommand())
	rootCmd.AddCommand(NewConvertCommand())
	rootCmd.AddCommand(NewSeekCommand())
	rootCmd.AddCommand(NewMountCommand())
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

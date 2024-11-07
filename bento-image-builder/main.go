package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"

	"github.com/klauspost/compress/zstd"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"github.com/jessevdk/go-flags"

	"github.com/bentoml/yatai-image-builder/common"
)

var (
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: true,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.SourceKey {
				source, _ := a.Value.Any().(*slog.Source)
				if source != nil {
					source.File = filepath.Base(source.File)
				}
			}
			return a
		},
	}))
)

type buildOptions struct {
	DockerfilePath string            `short:"f" long:"dockerfile" description:"Path to the dockerfile" required:"true"`
	ContextPath    string            `short:"c" long:"context" description:"Path to the context directory" required:"true"`
	Output         string            `short:"o" long:"output" description:"Path to the output file" required:"false"`
	BuildArg       map[string]string `long:"build-arg" description:"Build arg" required:"false"`
}

func build(ctx context.Context, opts buildOptions) error {
	dockerfileContent, err := os.ReadFile(opts.DockerfilePath)
	if err != nil {
		err = errors.Wrap(err, "failed to read dockerfile")
		return err
	}

	imageInfo, err := common.GetImageInfo(ctx, string(dockerfileContent), opts.ContextPath, opts.BuildArg)
	if err != nil {
		err = errors.Wrap(err, "failed to get image info")
		return err
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		err = errors.Wrap(err, "failed to create docker client")
		return err
	}

	logger.InfoContext(ctx, "pulling base image...", slog.String("image", imageInfo.BaseImage))
	pullOut, err := cli.ImagePull(ctx, imageInfo.BaseImage, image.PullOptions{})
	if err != nil {
		err = errors.Wrap(err, "failed to pull image")
		return err
	}
	defer pullOut.Close()

	logger.InfoContext(ctx, "base image pulled", slog.String("image", imageInfo.BaseImage))

	if _, err := io.Copy(os.Stdout, pullOut); err != nil {
		err = errors.Wrap(err, "failed to copy image pull output")
		return err
	}

	hostConfig := &container.HostConfig{
		AutoRemove: false,
		Binds:      []string{fmt.Sprintf("%s:%s", opts.ContextPath, opts.ContextPath)},
	}

	workDir := "/tmp/build-workdir"

	cmd := []string{"bash", "-c", strings.Join(append(append([]string{"set -ex", "mkdir -p " + workDir}, imageInfo.Commands...), "rm -rf "+workDir), ";\n")}

	logger.InfoContext(ctx, "creating container...", slog.String("image", imageInfo.BaseImage), slog.String("context", opts.ContextPath), slog.Any("cmd", cmd), slog.Any("env", imageInfo.Env), slog.String("working_dir", opts.ContextPath))

	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:      imageInfo.BaseImage,
		Cmd:        cmd,
		Env:        append(imageInfo.Env, fmt.Sprintf("CONTEXT=%s", opts.ContextPath)),
		WorkingDir: workDir,
		Tty:        true,
		OpenStdin:  true,
	}, hostConfig, nil, nil, imageInfo.Hash)
	if err != nil {
		err = errors.Wrap(err, "failed to create container")
		return err
	}

	logger.InfoContext(ctx, "container created", slog.String("image", imageInfo.BaseImage), slog.String("context", opts.ContextPath), slog.Any("cmd", cmd), slog.Any("env", imageInfo.Env), slog.String("working_dir", opts.ContextPath))

	err = cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
	if err != nil {
		err = errors.Wrap(err, "failed to start container")
		return err
	}

	logger.InfoContext(ctx, "container is running")

	logsOut, err := cli.ContainerLogs(ctx, resp.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: true})
	if err != nil {
		err = errors.Wrap(err, "failed to get container logs")
		return err
	}
	defer logsOut.Close()

	go func() {
		if _, err := io.Copy(os.Stdout, logsOut); err != nil {
			logger.ErrorContext(ctx, "failed to copy container logs", slog.String("error", err.Error()))
		}
	}()

	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			err = errors.Wrap(err, "failed to wait for container to exit")
			return err
		}
	case status := <-statusCh:
		if status.StatusCode != 0 {
			err := errors.Errorf("container exited with non-zero status: %d", status.StatusCode)
			return err
		}
	}

	exportOut, err := cli.ContainerExport(ctx, resp.ID)
	if err != nil {
		err = errors.Wrap(err, "failed to export container")
		return err
	}
	defer exportOut.Close()

	resultWriter := os.Stdout

	if opts.Output != "" {
		tarFile, err := os.Create(opts.Output)
		if err != nil {
			err = errors.Wrap(err, "failed to create tar file")
			return err
		}
		defer tarFile.Close()
		resultWriter = tarFile
	}

	enc, err := zstd.NewWriter(resultWriter, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		err = errors.Wrap(err, "failed to create zstd encoder")
		return err
	}
	defer enc.Close()

	logger.InfoContext(ctx, "compressing image...")
	_, err = io.Copy(enc, exportOut)
	if err != nil {
		err = errors.Wrap(err, "failed to copy and compress")
		return err
	}
	logger.InfoContext(ctx, "image compressed")
	return nil
}

func main() {
	ctx := context.Background()

	var opts buildOptions

	_, err := flags.Parse(&opts)
	if err != nil {
		os.Exit(1)
	}

	err = build(ctx, opts)
	if err != nil {
		logger.ErrorContext(ctx, "failed to build image", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

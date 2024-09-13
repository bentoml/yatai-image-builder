package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/bentoml/yatai-image-builder/pkg/common/blob"
	"github.com/bentoml/yatai-image-builder/pkg/common/envspec"
)

var baseCommands = []string{
	"groupadd -g 1034 -o bentoml",
	"useradd -m -u 1034 -g 1034 -o -r bentoml",
	"mkdir -p /home/bentoml/bento",
	"chown bentoml:bentoml /home/bentoml/bento -R",
	"echo 'export BENTO_PATH=/home/bentoml/bento' >> /etc/environment",
	"echo 'export BENTOML_HOME=/home/bentoml/' >> /etc/environment",
	"curl -LsSf https://astral.sh/uv/install.sh | sh",
	"mkdir -p /app",
	"uv venv /app/.venv",
	"chown bentoml:bentoml /app -R",
	". /app/.venv/bin/activate",
}

type ImageBuilder struct {
	dockerClient *client.Client
	blobStorage  blob.BlobStorage
}

func NewImageBuilder(dockerClient *client.Client, blobStorage blob.BlobStorage) *ImageBuilder {
	return &ImageBuilder{
		dockerClient: dockerClient,
		blobStorage:  blobStorage,
	}
}

func (ib *ImageBuilder) Build(ctx context.Context, spec envspec.EnvironmentSpec) error {
	// Generate layer name first
	layerName := spec.LayerName()

	logger := slog.With("baseImage", spec.BaseImage, "pythonVersion", spec.PythonVersion, "layerName", layerName)

	// Check if the layer already exists
	exists, err := ib.blobStorage.Exists(ctx, layerName)
	if err != nil {
		return fmt.Errorf("failed to check if layer exists: %v", err)
	}

	if exists {
		// Layer already exists, skip building
		logger.Info("Layer already exists. Skipping build.")
		return nil
	}

	// Layer doesn't exist, proceed with building
	logger.Info("Building layer")

	// 1. Create container based on base image
	logger.Info("Creating container")
	containerID, err := ib.createContainer(spec.BaseImage)
	if err != nil {
		logger.Error("Failed to create container", "error", err)
		return err
	}
	defer func() {
		logger.Info("Removing container")
		if err := ib.removeContainer(containerID); err != nil {
			logger.Error("Failed to remove container", "error", err)
			return
		}
		logger.Info("Container removed")
	}()

	logger = logger.With("containerID", containerID)
	logger.Info("Container created")

	// 2. Install specified Python version
	logger.Info("Installing Python")
	if err := ib.installPython(containerID, spec.PythonVersion); err != nil {
		logger.Error("Failed to install Python", "error", err)
		return err
	}
	logger.Info("Python installed")

	logger.Info("Executing commands")
	// 3. Execute commands
	allCommands := append(baseCommands, spec.Commands...)
	if err := ib.executeCommands(logger, containerID, allCommands); err != nil {
		logger.Error("Failed to execute commands", "error", err)
		return err
	}
	logger.Info("Commands executed")

	// 4. Install Python packages
	logger.Info("Installing Python packages")
	if err := ib.installPythonPackages(logger, containerID, spec.PythonPackages); err != nil {
		logger.Error("Failed to install Python packages", "error", err)
		return err
	}
	logger.Info("Python packages installed")

	// 5. Package and upload layer
	logger.Info("Packaging and uploading layer")
	if err := ib.packageAndUploadLayer(logger, containerID, layerName); err != nil {
		logger.Error("Failed to package and upload layer", "error", err)
		return err
	}

	logger.Info("Layer built and uploaded successfully.")
	return nil
}

func (ib *ImageBuilder) createContainer(baseImage string) (string, error) {
	ctx := context.Background()
	resp, err := ib.dockerClient.ContainerCreate(ctx, &container.Config{
		Image: baseImage,
		Cmd:   []string{"/bin/sh"},
		Tty:   true,
	}, nil, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("failed to create container: %v", err)
	}
	return resp.ID, nil
}

func (ib *ImageBuilder) removeContainer(containerID string) error {
	ctx := context.Background()
	return ib.dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
}

func (ib *ImageBuilder) installPython(containerID, version string) error {
	ctx := context.Background()
	cmd := fmt.Sprintf("uv python install %s", version)
	exec, err := ib.dockerClient.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd: []string{"/bin/sh", "-c", cmd},
	})
	if err != nil {
		return fmt.Errorf("failed to create exec command: %v", err)
	}

	if err := ib.dockerClient.ContainerExecStart(ctx, exec.ID, container.ExecStartOptions{}); err != nil {
		return fmt.Errorf("failed to execute command: %v", err)
	}

	return nil
}

func (ib *ImageBuilder) executeCommands(logger *slog.Logger, containerID string, commands []string) error {
	ctx := context.Background()
	for _, cmd := range commands {
		logger.Info("Executing command", "command", cmd)
		exec, err := ib.dockerClient.ContainerExecCreate(ctx, containerID, container.ExecOptions{
			Cmd: []string{"/bin/sh", "-c", cmd},
		})
		if err != nil {
			logger.Error("Failed to create exec command", "error", err)
			return fmt.Errorf("failed to create exec command: %v", err)
		}
		logger.Info("Exec command created", "command", cmd)

		logger.Info("Starting exec command", "command", cmd)
		if err := ib.dockerClient.ContainerExecStart(ctx, exec.ID, container.ExecStartOptions{}); err != nil {
			logger.Error("Failed to execute command", "error", err)
			return fmt.Errorf("failed to execute command: %v", err)
		}
		logger.Info("Exec command completed", "command", cmd)
	}

	return nil
}

func (ib *ImageBuilder) installPythonPackages(logger *slog.Logger, containerID string, packages []string) error {
	ctx := context.Background()
	cmd := fmt.Sprintf("uv pip install %s", strings.Join(packages, " "))
	logger.Info("Installing Python packages", "command", cmd)
	exec, err := ib.dockerClient.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd: []string{"/bin/sh", "-c", cmd},
	})
	if err != nil {
		logger.Error("Failed to create exec command", "error", err)
		return fmt.Errorf("failed to create exec command: %v", err)
	}

	if err := ib.dockerClient.ContainerExecStart(ctx, exec.ID, container.ExecStartOptions{}); err != nil {
		logger.Error("Failed to execute command", "error", err)
		return fmt.Errorf("failed to execute command: %v", err)
	}
	logger.Info("Exec command completed", "command", cmd)

	return nil
}

func (ib *ImageBuilder) packageAndUploadLayer(logger *slog.Logger, containerID, layerName string) error {
	ctx := context.Background()

	// Get container filesystem changes
	changes, err := ib.dockerClient.ContainerDiff(ctx, containerID)
	if err != nil {
		logger.Error("Failed to get container changes", "error", err)
		return fmt.Errorf("failed to get container changes: %v", err)
	}

	// Create a tar file
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, change := range changes {
		if change.Kind == container.ChangeAdd || change.Kind == container.ChangeModify {
			// Copy file from container
			reader, _, err := ib.dockerClient.CopyFromContainer(ctx, containerID, change.Path)
			if err != nil {
				logger.Error("Failed to copy file from container", "error", err)
				return fmt.Errorf("failed to copy file from container: %v", err)
			}
			defer reader.Close()

			// Add file to tar
			tr := tar.NewReader(reader)
			header, err := tr.Next()
			if err != nil {
				if err == io.EOF {
					continue
				}
				logger.Error("Failed to read tar header", "error", err)
				return fmt.Errorf("failed to read tar header: %v", err)
			}

			if err := tw.WriteHeader(header); err != nil {
				logger.Error("Failed to write tar header", "error", err)
				return fmt.Errorf("failed to write tar header: %v", err)
			}

			if _, err := io.Copy(tw, tr); err != nil {
				logger.Error("Failed to copy file content", "error", err)
				return fmt.Errorf("failed to copy file content: %v", err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		logger.Error("Failed to close tar writer", "error", err)
		return fmt.Errorf("failed to close tar writer: %v", err)
	}

	// Upload to blob storage
	key := fmt.Sprintf("%s-%s.tar", containerID, layerName)
	logger.Info("Uploading layer to blob storage", "key", key)
	if err := ib.blobStorage.Upload(ctx, key, bytes.NewReader(buf.Bytes())); err != nil {
		logger.Error("Failed to upload to blob storage", "error", err)
		return fmt.Errorf("failed to upload to blob storage: %v", err)
	}
	logger.Info("Layer uploaded to blob storage", "key", key)

	return nil
}

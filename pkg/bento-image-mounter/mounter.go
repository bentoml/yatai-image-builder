package main

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"log/slog"

	"github.com/pkg/errors"

	"github.com/bentoml/yatai-image-builder/pkg/common/blob"
	"github.com/bentoml/yatai-image-builder/pkg/common/envspec"
)

type ImageMounter struct {
	blobStorage blob.BlobStorage
}

func NewImageMounter(blobStorage blob.BlobStorage) *ImageMounter {
	return &ImageMounter{
		blobStorage: blobStorage,
	}
}

func (m *ImageMounter) Mount(ctx context.Context, spec envspec.EnvironmentSpec, lowerDir, destDir string) error {
	layerName := spec.LayerName()
	logger := slog.With("baseImage", spec.BaseImage, "pythonVersion", spec.PythonVersion, "layerName", layerName)

	exists, err := m.blobStorage.Exists(ctx, layerName)
	if err != nil {
		logger.Error("Failed to check if layer exists", "error", err)
		return err
	}

	if !exists {
		logger.Error("Layer does not exist on blob storage")
		return errors.New("layer does not exist on blob storage")
	}

	logger.Info("Layer exists on blob storage")

	layerDir := filepath.Join("/layers/", layerName)
	// if layerDir already exists, skip downloading
	if _, err := os.Stat(layerDir); err == nil {
		logger.Info("Layer directory already exists, skipping download")
	} else {
		tempLayerDir := filepath.Join("/layers/", "."+layerName)
		err = os.MkdirAll(tempLayerDir, 0755)
		if err != nil {
			logger.Error("Failed to create temp layer directory", "error", err)
			return err
		}
		defer os.RemoveAll(tempLayerDir)

		logger.Info("Downloading layer")
		reader, err := m.blobStorage.Download(ctx, layerName)
		if err != nil {
			logger.Error("Failed to download layer", "error", err)
			return err
		}
		defer reader.Close()

		// streaming untar
		tarReader := tar.NewReader(reader)

		for {
			header, err := tarReader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				logger.Error("Failed to read tar file", "error", err)
				return err
			}

			target := filepath.Join(tempLayerDir, header.Name)
			switch header.Typeflag {
			case tar.TypeDir:
				if err := os.MkdirAll(target, header.FileInfo().Mode()); err != nil {
					logger.Error("Failed to create directory", "error", err)
					return err
				}
			case tar.TypeReg:
				file, err := os.Create(target)
				if err != nil {
					logger.Error("Failed to create file", "error", err)
					return err
				}
				if _, err := io.Copy(file, tarReader); err != nil {
					logger.Error("Failed to copy file", "error", err)
					return err
				}
				file.Close()
			case tar.TypeSymlink:
				if err := os.Symlink(header.Linkname, target); err != nil {
					logger.Error("Failed to create symlink", "error", err)
					return err
				}
			case tar.TypeXGlobalHeader:
				// skip
			default:
				logger.Error("Unsupported file type", "type", header.Typeflag)
				return errors.New("unsupported file type")
			}
		}

		logger.Info("Layer downloaded")

		err = os.Rename(tempLayerDir, layerDir)
		if err != nil {
			logger.Error("Failed to move layer to layer directory", "error", err)
			return err
		}
	}

	upperDir := "/data/upper"
	err = os.MkdirAll(upperDir, 0755)
	if err != nil {
		logger.Error("Failed to create upper directory", "error", err)
		return err
	}

	workDir := "/data/work"
	err = os.MkdirAll(workDir, 0755)
	if err != nil {
		logger.Error("Failed to create work directory", "error", err)
		return err
	}

	// mount overlay
	overlayCmd := exec.Command("mount", "-t", "overlay", "-o", "lowerdir="+lowerDir+":"+layerDir+",upperdir="+upperDir+",workdir="+workDir, "overlay", destDir)
	logger = logger.With("overlayCmd", overlayCmd)
	logger.Info("Mounting overlay")
	overlayCmd.Stdout = os.Stdout
	overlayCmd.Stderr = os.Stderr
	err = overlayCmd.Run()
	if err != nil {
		logger.Error("Failed to mount overlay", "error", err)
		return err
	}
	logger.Info("Overlay mounted")

	<-ctx.Done()

	return nil
}

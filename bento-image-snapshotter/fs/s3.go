package fs

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/containerd/log"
	"github.com/klauspost/compress/zstd"
	"github.com/pkg/errors"
)

const (
	BucketLabel       = "containerd.io/snapshot/bento-image-bucket"
	ObjectKeyLabel    = "containerd.io/snapshot/bento-image-object-key"
	IsBentoLayerLabel = "containerd.io/snapshot/bento-image-is-bento-layer"
)

type S3FileSystem struct{}

func NewS3FileSystem() S3FileSystem {
	return S3FileSystem{}
}

func (o S3FileSystem) Mount(ctx context.Context, mountpoint string, labels map[string]string) error {
	if err := os.MkdirAll(mountpoint, 0755); err != nil {
		return errors.Wrap(err, "failed to create mountpoint")
	}

	bucketName := labels[BucketLabel]
	objectKey := labels[ObjectKeyLabel]
	logger := log.G(ctx).WithField("bucket", bucketName).WithField("key", objectKey).WithField("mountpoint", mountpoint)
	logger.Info("downloading layer from S3")
	if err := o.downloadLayerFromS3(ctx, bucketName, objectKey, mountpoint); err != nil {
		return errors.Wrap(err, "failed to download layer from S3")
	}
	isBentoLayer := labels[IsBentoLayerLabel] == "true"
	if isBentoLayer {
		logger.Info("chmoding entrypoint.sh")
		err := os.Chmod(filepath.Join(mountpoint, "home/bentoml/bento/env/docker/entrypoint.sh"), 0755)
		if err != nil {
			return errors.Wrap(err, "failed to chmod entrypoint.sh")
		}
		logger.Info("chmoding entrypoint.sh done")
	}
	return nil
}

func (o S3FileSystem) Check(ctx context.Context, mountpoint string, labels map[string]string) error {
	var exists bool
	var err error
	if exists, err = fileExists(mountpoint); err != nil {
		return errors.Wrap(err, "failed to check mountpoint")
	}

	if !exists {
		return errors.New("mountpoint does not exist")
	}

	return nil
}

func (o S3FileSystem) Unmount(ctx context.Context, mountpoint string) error {
	if err := os.RemoveAll(mountpoint); err != nil {
		return errors.Wrap(err, "failed to remove mountpoint")
	}
	return nil
}

func stringifyCmd(cmd *exec.Cmd) string {
	cmdStr := cmd.Path
	for _, arg := range cmd.Args {
		cmdStr += " " + arg
	}
	return cmdStr
}

func pipeline(cmds ...*exec.Cmd) error {
	if len(cmds) == 0 {
		return nil
	}

	stderrs := make([]*bytes.Buffer, len(cmds))
	for i := 0; i < len(cmds); i++ {
		stderrs[i] = bytes.NewBuffer(nil)
		cmds[i].Stderr = stderrs[i]
	}

	collectStderrs := func() string {
		var stderrsStr string
		for idx, stderr := range stderrs {
			stderrsStr += fmt.Sprintf("stderr of %s:\n%s\n", stringifyCmd(cmds[idx]), stderr.String())
		}

		return stderrsStr
	}

	for i := 0; i < len(cmds)-1; i++ {
		stdout, err := cmds[i].StdoutPipe()
		if err != nil {
			return err
		}
		cmds[i+1].Stdin = stdout
	}

	cmds[len(cmds)-1].Stdout = os.Stdout

	for _, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			return err
		}
	}

	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			return errors.Wrapf(err, "failed to execute command: %s; %s", stringifyCmd(cmd), collectStderrs())
		}
	}

	return nil
}

func streamingDownloadAndExtractTar(ctx context.Context, s3Path string, destPath string) error {
	s5cmdPath, err := exec.LookPath("s5cmd")
	if err != nil || s5cmdPath == "" {
		return errors.Wrap(err, "s5cmd not found in PATH")
	}
	pzstdPath, err := exec.LookPath("pzstd")
	if err != nil || pzstdPath == "" {
		return errors.Wrap(err, "pzstd not found in PATH")
	}
	tarPath, err := exec.LookPath("tar")
	if err != nil || tarPath == "" {
		return errors.Wrap(err, "tar not found in PATH")
	}

	s3EndpointURL := os.Getenv("S3_ENDPOINT_URL")
	if s3EndpointURL == "" {
		return errors.New("S3_ENDPOINT_URL not set")
	}

	s5cmdCmd := exec.CommandContext(ctx, s5cmdPath, "--endpoint-url", s3EndpointURL, "cat", s3Path)
	s5cmdCmd.Dir = destPath
	pzstdCmd := exec.CommandContext(ctx, pzstdPath, "-d")
	pzstdCmd.Dir = destPath
	tarCmd := exec.CommandContext(ctx, tarPath, "-xf", "-")
	tarCmd.Dir = destPath

	err = pipeline(s5cmdCmd, pzstdCmd, tarCmd)
	if err != nil {
		return errors.Wrap(err, "failed to start s5cmd")
	}
	return nil
}

func (o *S3FileSystem) downloadLayerFromS3(ctx context.Context, bucketName, layerKey string, destinationPath string) error {
	baseName := filepath.Base(destinationPath)
	dirName := filepath.Dir(destinationPath)
	tempName := filepath.Join(dirName, baseName+".tmp")

	if err := os.MkdirAll(tempName, 0755); err != nil {
		return errors.Wrap(err, "failed to create temp directory")
	}

	logger := log.G(ctx).WithField("key", layerKey).WithField("destinationPath", tempName)
	logger.Info("downloading and streaming decompression of the layer from S3...")

	s3Path := fmt.Sprintf("s3://%s/%s", bucketName, layerKey)
	err := streamingDownloadAndExtractTar(ctx, s3Path, tempName)
	if err != nil {
		return errors.Wrap(err, "failed to download layer from S3")
	}

	logger.Info("the layer has been downloaded and decompressed")

	_ = os.RemoveAll(destinationPath)

	logger = log.G(ctx).WithField("key", layerKey).WithField("destinationPath", destinationPath).WithField("oldPath", tempName)
	logger.Info("renaming the layer...")

	if err := os.Rename(tempName, destinationPath); err != nil {
		return errors.Wrap(err, "failed to rename layer")
	}

	logger.Info("the layer has been renamed")

	return nil
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// streamExtractTar extracts a tar stream (potentially compressed) to a destination path
func streamExtractTar(ctx context.Context, reader io.Reader, destPath string) error {
	logger := log.G(ctx).WithField("destPath", destPath)

	var tr *tar.Reader

	zr, err := zstd.NewReader(reader)
	if err != nil {
		return errors.Wrap(err, "failed to create zstd reader")
	}
	defer zr.Close()
	tr = tar.NewReader(zr)

	// Track created directories
	createdDirs := make(map[string]struct{})

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.Wrap(err, "failed to read tar header")
		}

		// Clean path to prevent directory traversal attacks
		target := filepath.Join(destPath, filepath.Clean(header.Name))
		if !strings.HasPrefix(target, destPath) {
			return errors.New("invalid tar path")
		}

		// Get parent directory
		dir := filepath.Dir(target)
		if _, exists := createdDirs[dir]; !exists {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return errors.Wrap(err, "failed to create directory")
			}
			createdDirs[dir] = struct{}{}
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := handleDirectory(target, header.Mode); err != nil {
				return err
			}
			createdDirs[target] = struct{}{}

		case tar.TypeReg:
			if err := handleRegularFile(target, tr, header.Mode); err != nil {
				return err
			}

		case tar.TypeSymlink:
			if err := handleSymlink(target, header.Linkname); err != nil {
				return err
			}

		case tar.TypeLink:
			if err := handleHardLink(target, filepath.Join(destPath, header.Linkname)); err != nil {
				return err
			}

		case tar.TypeChar:
			// Skip character devices
			logger.Warnf("Skipping character device: %s", header.Name)
			continue

		case tar.TypeBlock:
			// Skip block devices
			logger.Warnf("Skipping block device: %s", header.Name)
			continue

		case tar.TypeFifo:
			// Skip named pipes
			logger.Warnf("Skipping named pipe: %s", header.Name)
			continue

		default:
			logger.Warnf("Skipping unknown type %d for %s", header.Typeflag, header.Name)
			continue
		}
	}

	return nil
}

// Handle directory creation
func handleDirectory(path string, mode int64) error {
	if err := os.MkdirAll(path, os.FileMode(mode)); err != nil {
		return errors.Wrap(err, "failed to create directory")
	}
	return nil
}

// Handle regular file creation and content copying
func handleRegularFile(path string, reader io.Reader, mode int64) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(mode))
	if err != nil {
		return errors.Wrap(err, "failed to create file")
	}
	defer file.Close()

	if _, err := io.Copy(file, reader); err != nil {
		return errors.Wrap(err, "failed to write file")
	}
	return nil
}

// Handle symbolic link creation
func handleSymlink(path, linkname string) error {
	if err := os.Symlink(linkname, path); err != nil {
		return errors.Wrap(err, "failed to create symlink")
	}
	return nil
}

// Handle hard link creation
func handleHardLink(path, linkname string) error {
	if err := os.Link(linkname, path); err != nil {
		return errors.Wrap(err, "failed to create hard link")
	}
	return nil
}

package fs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/containerd/log"
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

func chownRecursive(ctx context.Context, root string, uid, gid int) error {
	return errors.Wrap(filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return errors.Wrap(err, "failed to walk the directory")
		}
		// Change ownership of the file/directory
		err = os.Chown(path, uid, gid)
		if err != nil {
			log.G(ctx).Errorf("failed to chown %s: %v", path, err)
			return errors.Wrap(err, "failed to chown the file/directory")
		}
		return nil
	}), "failed to walk the directory")
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
		logger.Info("chowning the /home/bentoml directory")
		if err := chownRecursive(ctx, filepath.Join(mountpoint, "home", "bentoml"), 1034, 1034); err != nil {
			return errors.Wrap(err, "failed to chown /home/bentoml directory")
		}
		logger.Info("successfully chowned the /home/bentoml directory")
	}
	logger.Info("layer downloaded from S3")
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

	startTime := time.Now()
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("s5cmd cat %s | pzstd -d | tar -xf -", s3Path)) // nolint:gosec
	cmd.Dir = tempName

	err := cmd.Run()
	if err != nil {
		return errors.Wrapf(err, "failed to run command: %s", stringifyCmd(cmd))
	}

	duration := time.Since(startTime)

	logger.WithField("duration", duration).Info("the layer has been downloaded and decompressed")

	err = os.RemoveAll(destinationPath)
	if err != nil && !os.IsNotExist(err) {
		return errors.Wrapf(err, "failed to remove destination path: %s", destinationPath)
	}

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
	return false, errors.Wrap(err, "failed to stat file")
}

package fs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/log"
	"github.com/pkg/errors"

	"github.com/bentoml/yatai-image-builder/common"
)

type S3FileSystem struct {
	EnableRamfs bool
}

func NewS3FileSystem(enableRamfs bool) S3FileSystem {
	return S3FileSystem{
		EnableRamfs: enableRamfs,
	}
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

func (o S3FileSystem) getObjectSize(ctx context.Context, bucketName, objectKey string) (int64, error) {
	s3Path := fmt.Sprintf("s3://%s/%s", bucketName, objectKey)
	cmd := exec.CommandContext(ctx, "s5cmd", "ls", s3Path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return 0, errors.Wrapf(err, "failed to run command: %s, stderr: %s", stringifyCmd(cmd), stderr.String())
	}
	fields := strings.Fields(string(output))
	if len(fields) < 3 {
		return 0, errors.Errorf("failed to parse output: %s", string(output))
	}
	size, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to parse size: %s", string(output))
	}
	return size, nil
}

func (o S3FileSystem) Mount(ctx context.Context, mountpoint string, labels map[string]string) error {
	if err := os.MkdirAll(mountpoint, 0755); err != nil {
		return errors.Wrap(err, "failed to create mountpoint")
	}

	bucketName := labels[common.DescriptorAnnotationBucket]
	objectKey := labels[common.DescriptorAnnotationObjectKey]
	logger := log.G(ctx).WithField("bucket", bucketName).WithField("key", objectKey).WithField("mountpoint", mountpoint)

	if o.EnableRamfs {
		objectSize, err := o.getObjectSize(ctx, bucketName, objectKey)
		if err != nil {
			return errors.Wrap(err, "failed to get object size")
		}
		logger.Infof("object size: %d", objectSize)
		ramfsSize := int64(float64(objectSize) * 2.2)
		logger.Infof("mounting ramfs with size: %d", ramfsSize)
		cmd := exec.CommandContext(ctx, "mount", "-t", "ramfs", "-o", "size="+strconv.FormatInt(ramfsSize, 10), "ramfs", mountpoint) // nolint:gosec
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return errors.Wrap(err, "failed to mount ramfs")
		}
		logger.Info("successfully mounted ramfs")
	}

	logger.Info("downloading layer from S3")
	if err := o.downloadLayerFromS3(ctx, bucketName, objectKey, mountpoint); err != nil {
		return errors.Wrap(err, "failed to download layer from S3")
	}
	isBentoLayer := labels[common.DescriptorAnnotationIsBentoLayer] == "true"
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

func isRamfsMounted(path string) (bool, error) {
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return false, errors.Wrap(err, "failed to open /proc/mounts")
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		mountPoint := fields[1]
		fsType := fields[2]

		if mountPoint == path && fsType == "ramfs" {
			return true, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return false, errors.Wrap(err, "failed to scan /proc/mounts")
	}

	return false, nil
}

func unmountRamfs(path string) error {
	cmd := exec.Command("umount", path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return errors.Wrap(cmd.Run(), "failed to unmount ramfs")
}

func (o S3FileSystem) Unmount(ctx context.Context, mountpoint string) error {
	if ramfsMounted, err := isRamfsMounted(mountpoint); err != nil {
		return errors.Wrap(err, "failed to check if ramfs is mounted")
	} else if ramfsMounted {
		if err := unmountRamfs(mountpoint); err != nil {
			return errors.Wrap(err, "failed to unmount ramfs")
		}
	}
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
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("s5cmd cat %s | pzstd -d | tar -xf -", s3Path)) // nolint:gosec
	cmd.Stderr = &stderr
	cmd.Dir = tempName

	err := cmd.Run()
	if err != nil {
		return errors.Wrapf(err, "failed to run command: %s, stderr: %s", stringifyCmd(cmd), stderr.String())
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

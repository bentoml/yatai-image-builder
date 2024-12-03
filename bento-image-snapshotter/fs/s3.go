package fs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/containerd/log"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"

	"github.com/bentoml/yatai-image-builder/common"
)

type S3FileSystem struct {
	EnableRamfs bool
	client      *s3.Client
}

func NewS3FileSystem(ctx context.Context, enableRamfs bool) (S3FileSystem, error) {
	client, err := GetS3Client(ctx, true)
	if err != nil {
		log.G(ctx).WithError(err).Fatal("failed to create s3 client")
		return S3FileSystem{}, errors.Wrap(err, "failed to create s3 client")
	}
	return S3FileSystem{
		EnableRamfs: enableRamfs,
		client:      client,
	}, nil
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
	bucketName := labels[common.DescriptorAnnotationBucket]
	objectKey := labels[common.DescriptorAnnotationObjectKey]
	logger := log.G(ctx).WithField("bucket", bucketName).WithField("key", objectKey).WithField("mountpoint", mountpoint)
	destinationPath := mountpoint

	if o.EnableRamfs {
		destinationPath = getRamfsPath(mountpoint)
		if err := os.MkdirAll(destinationPath, 0755); err != nil {
			return errors.Wrap(err, "failed to create mountpoint")
		}

		objectSize, err := o.getObjectSize(ctx, bucketName, objectKey)
		if err != nil {
			return errors.Wrap(err, "failed to get object size")
		}
		logger.Infof("object size: %d", objectSize)
		ramfsSize := int64(float64(objectSize) * 2.2)
		logger.Infof("mounting ramfs with size: %d", ramfsSize)
		cmd := exec.CommandContext(ctx, "mount", "-t", "ramfs", "-o", "size="+strconv.FormatInt(ramfsSize, 10), "ramfs", destinationPath) // nolint:gosec
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return errors.Wrap(err, "failed to mount ramfs")
		}
		logger.Info("successfully mounted ramfs")
	}

	logger.Info("downloading layer from S3")
	if err := o.downloadLayerFromS3(ctx, bucketName, objectKey, destinationPath); err != nil {
		return errors.Wrap(err, "failed to download layer from S3")
	}
	isBentoLayer := labels[common.DescriptorAnnotationIsBentoLayer] == "true"
	if isBentoLayer {
		logger.Info("chowning the /home/bentoml directory")
		if err := chownRecursive(ctx, filepath.Join(destinationPath, "home", "bentoml"), 1034, 1034); err != nil {
			return errors.Wrap(err, "failed to chown /home/bentoml directory")
		}
		logger.Info("successfully chowned the /home/bentoml directory")
	}
	logger.Info("layer downloaded from S3")
	if o.EnableRamfs {
		if err := os.RemoveAll(mountpoint); err != nil {
			if !os.IsNotExist(err) {
				return errors.Wrap(err, "failed to remove mountpoint")
			}
		}
		// symlink the destinationPath to the mountpoint
		if err := os.Symlink(destinationPath, mountpoint); err != nil {
			return errors.Wrap(err, "failed to symlink destinationPath to mountpoint")
		}
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

func getRamfsPath(path string) string {
	if strings.HasSuffix(path, "/") {
		return path[:len(path)-1] + ".ramfs"
	}
	return path + ".ramfs"
}

func isRamfsMounted(path string) (bool, error) {
	ramfsPath := getRamfsPath(path)
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

		if mountPoint == ramfsPath && fsType == "ramfs" {
			return true, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return false, errors.Wrap(err, "failed to scan /proc/mounts")
	}

	return false, nil
}

func unmountRamfs(path string) error {
	ramfsPath := getRamfsPath(path)
	cmd := exec.Command("umount", ramfsPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, "failed to unmount ramfs")
	}
	if err := os.RemoveAll(ramfsPath); err != nil {
		if !os.IsNotExist(err) {
			return errors.Wrap(err, "failed to remove ramfs")
		}
	}
	return nil
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

func downloadRange(ctx context.Context, presignedURL string, start, end int64, writer io.WriterAt) error {
	// use curl
	logger := log.G(ctx).WithField("range", fmt.Sprintf("%d-%d", start, end)).WithField("url", presignedURL)
	logger.Debug("downloading range...")
	pr, pw := io.Pipe()
	go func() {
		var stderr bytes.Buffer
		defer pw.Close()
		cmd := exec.CommandContext(ctx, "curl", "--silent", "--show-error", "--fail", "--output", "-", "--range", fmt.Sprintf("%d-%d", start, end), presignedURL) // nolint:gosec
		cmd.Stderr = &stderr
		cmd.Stdout = pw
		if err := cmd.Run(); err != nil {
			pw.CloseWithError(errors.Wrapf(err, "failed to run command: %s, stderr: %s", stringifyCmd(cmd), stderr.String()))
		}
	}()
	buf := make([]byte, end-start+1)
	for {
		n, err := pr.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return errors.Wrap(err, "failed to read from pipe")
		}
		_, err = writer.WriteAt(buf[:n], start)
		if err != nil {
			return errors.Wrap(err, "failed to write to writer")
		}
		start += int64(n)
	}
	logger.Debug("range downloaded")
	return nil
}

func parrallelDownload(ctx context.Context, presignedURL string, destPath string, parallelism int) error {
	// use curl to get the size of the file
	logger := log.G(ctx).WithField("url", presignedURL)
	logger.Debug("getting the size of the file...")
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "curl", "--silent", "--show-error", "--fail", "--head", "--output", "-", presignedURL) // nolint:gosec
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return errors.Wrapf(err, "failed to run command: %s, stderr: %s", stringifyCmd(cmd), stderr.String())
	}
	var size int64
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if strings.ToLower(strings.TrimSpace(k)) == "content-length" {
			var err error
			size, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return errors.Wrapf(err, "failed to parse size: %s", string(output))
			}
			logger.Debugf("size: %d", size)
			break
		}
	}

	if size == 0 {
		return errors.New("cannot get the file size from presigned url")
	}

	partSize := size / int64(parallelism)
	if size%int64(parallelism) > 0 {
		partSize += 1
	}

	baseDir := filepath.Dir(destPath)
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return errors.Wrap(err, "failed to create base dir")
	}

	// create the target file and falloc the target file
	file, err := os.Create(destPath)
	if err != nil {
		return errors.Wrap(err, "failed to create target file")
	}
	defer file.Close()

	// use truncate to fallocate the target file
	// seek to the first byte of the file
	if _, err := file.Seek(0, 0); err != nil {
		return errors.Wrap(err, "failed to seek the target file")
	}
	// truncate the file to the size
	if err := file.Truncate(size); err != nil {
		return errors.Wrap(err, "failed to truncate the target file")
	}
	//
	// if err := syscall.Fallocate(int(file.Fd()), 0, 0, size); err != nil {
	// 	return errors.Wrap(err, "failed to fallocate the target file")
	// }

	var eg errgroup.Group
	for i := 0; i < parallelism; i++ {
		start := int64(i) * partSize
		end := start + partSize - 1
		if end >= size {
			end = size - 1
		}
		eg.Go(func() error {
			defer func() {
				if err := recover(); err != nil {
					if err_, ok := err.(error); ok {
						log.G(ctx).WithError(err_).Error("recovered from panic")
					}
				}
			}()
			return downloadRange(ctx, presignedURL, start, end, file)
		})
	}
	return errors.Wrap(eg.Wait(), "downloading")
}

func (o *S3FileSystem) downloadLayerFromS3(ctx context.Context, bucketName, layerKey string, destinationPath string) error {
	baseName := filepath.Base(destinationPath)
	dirName := filepath.Dir(destinationPath)
	downloadedFilePath := filepath.Join(dirName, "downloads", layerKey+".tar.zst")
	tempName := filepath.Join(dirName, baseName+".tmp")

	defer func() {
		if err := os.Remove(downloadedFilePath); err != nil {
			if !os.IsNotExist(err) {
				log.G(ctx).WithError(err).Error("failed to remove downloaded file")
			}
		}
	}()

	if o.EnableRamfs {
		tempName = destinationPath
	} else {
		if err := os.MkdirAll(tempName, 0755); err != nil {
			return errors.Wrap(err, "failed to create temp directory")
		}
	}

	logger := log.G(ctx).WithField("key", layerKey).WithField("destinationPath", tempName)

	presignedURL, err := GetS3PresignedURL(ctx, o.client, bucketName, layerKey)
	if err != nil {
		return errors.Wrap(err, "failed to get s3 presigned url")
	}

	coreNumber := runtime.NumCPU()

	startTime := time.Now()
	logger.Info("downloading layer from S3...")
	err = parrallelDownload(ctx, presignedURL, downloadedFilePath, coreNumber)
	if err != nil {
		return errors.Wrap(err, "failed to download layer from S3")
	}

	downloadDuration := time.Since(startTime)
	logger.WithField("duration", downloadDuration).Info("the layer has been downloaded from S3")

	startTime = time.Now()
	logger.Info("decompressing layer...")
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("pzstd -d -c %s | tar -xf -", downloadedFilePath)) // nolint:gosec
	cmd.Stderr = &stderr
	cmd.Dir = tempName

	err = cmd.Run()
	if err != nil {
		return errors.Wrapf(err, "failed to run command: %s, stderr: %s", stringifyCmd(cmd), stderr.String())
	}

	decompressionDuration := time.Since(startTime)

	logger.WithField("duration", decompressionDuration).Info("the layer has been decompressed")

	if !o.EnableRamfs {
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
	}

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

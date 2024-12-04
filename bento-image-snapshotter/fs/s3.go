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
	"slices"
	"strconv"
	"strings"
	"sync"
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
	if mounted, err := o.isMounted(mountpoint); err != nil {
		return errors.Wrap(err, "failed to check if mountpoint is mounted")
	} else if mounted {
		log.G(ctx).WithField("mountpoint", mountpoint).Info("already mounted")
		return nil
	}

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
	return createMountedFlag(mountpoint)
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

func getMountedFlagPath(mountpoint string) string {
	baseDir := filepath.Dir(mountpoint)
	return filepath.Join(baseDir, ".mounted")
}

func createMountedFlag(mountpoint string) error {
	mountedFlagPath := getMountedFlagPath(mountpoint)
	file, err := os.Create(mountedFlagPath)
	if err != nil {
		return errors.Wrap(err, "failed to create mounted flag")
	}
	defer file.Close()
	return nil
}

func removeMountedFlag(mountpoint string) error {
	mountedFlagPath := getMountedFlagPath(mountpoint)
	if err := os.Remove(mountedFlagPath); err != nil {
		if !os.IsNotExist(err) {
			return errors.Wrap(err, "failed to remove mounted flag")
		}
	}
	return nil
}

func (o S3FileSystem) isMounted(mountpoint string) (bool, error) {
	if o.EnableRamfs {
		return isRamfsMounted(mountpoint)
	}
	mountedFlagPath := getMountedFlagPath(mountpoint)
	if _, err := os.Stat(mountedFlagPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, errors.Wrap(err, "failed to stat mounted flag")
	}
	return true, nil
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
	return removeMountedFlag(mountpoint)
}

func stringifyCmd(cmd *exec.Cmd) string {
	cmdStr := cmd.Path
	for _, arg := range cmd.Args {
		cmdStr += " " + arg
	}
	return cmdStr
}

func downloadRange(ctx context.Context, presignedURL string, start, end int64, writer io.WriterAt) error {
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

	buf := make([]byte, 32*1024)
	currentPos := start

	for {
		n, err := pr.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return errors.Wrap(err, "failed to read from pipe")
		}

		_, err = writer.WriteAt(buf[:n], currentPos)
		if err != nil {
			return errors.Wrap(err, "failed to write to writer")
		}
		currentPos += int64(n)
	}

	logger.Debug("range downloaded")
	return nil
}

func parrallelDownload(ctx context.Context, presignedURL string, writer io.WriterAt, size int64, parallelism int) error {
	partSize := size / int64(parallelism)
	if size%int64(parallelism) > 0 {
		partSize += 1
	}

	eg, ctx := errgroup.WithContext(ctx)
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
			err := downloadRange(ctx, presignedURL, start, end, writer)
			if err != nil {
				return errors.Wrap(err, "failed to download range")
			}
			return nil
		})
	}

	return errors.Wrap(eg.Wait(), "failed to download layer from S3")
}

func getObjectSizeFromPresignedURL(ctx context.Context, presignedURL string) (int64, error) {
	// use curl to get the size of the file
	logger := log.G(ctx).WithField("url", presignedURL)
	logger.Debug("getting the size of the file...")
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "curl", "--silent", "--show-error", "--fail", "--head", "--output", "-", presignedURL) // nolint:gosec
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return 0, errors.Wrapf(err, "failed to run command: %s, stderr: %s", stringifyCmd(cmd), stderr.String())
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
				return 0, errors.Wrapf(err, "failed to parse size: %s", string(output))
			}
			logger.Debugf("size: %d", size)
			break
		}
	}

	if size == 0 {
		return 0, errors.New("cannot get the file size from presigned url")
	}
	return size, nil
}

func ParrallelDownload(ctx context.Context, presignedURL string, downloadedFilePath string) error {
	size, err := getObjectSizeFromPresignedURL(ctx, presignedURL)

	if err != nil {
		return errors.Wrap(err, "failed to get object size")
	}

	coreNumber := runtime.NumCPU()

	baseDir := filepath.Dir(downloadedFilePath)
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return errors.Wrap(err, "failed to create base dir for downloaded file")
	}

	_, err = os.OpenFile(downloadedFilePath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return errors.Wrap(err, "failed to create downloaded file")
	}

	file, err := os.OpenFile(downloadedFilePath, os.O_RDWR, 0644)
	if err != nil {
		return errors.Wrap(err, "failed to reopen file with O_DIRECT")
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

	startTime := time.Now()
	err = parrallelDownload(ctx, presignedURL, file, size, coreNumber)
	if err != nil {
		return errors.Wrap(err, "failed to download layer from S3")
	}

	duration := time.Since(startTime)

	bandwidth := float64(size) / 1024 / 1024 / duration.Seconds()
	log.G(ctx).WithField("duration", duration).WithField("bandwidth", fmt.Sprintf("%.2f MB/s", bandwidth)).Info("the file has been downloaded and decompressed")

	return nil
}

type DataRange struct {
	Start int64 // inclusive
	End   int64 // inclusive
}

type WriterAtReader struct {
	file           *os.File
	readyDataRange []DataRange
	eofReached     bool
	lock           sync.RWMutex
	cond           *sync.Cond
	curOffset      int64
	size           int64
}

func NewWriterAtReader(file *os.File, size int64) *WriterAtReader {
	w := &WriterAtReader{
		file:           file,
		readyDataRange: make([]DataRange, 0),
		size:           size,
	}
	w.cond = sync.NewCond(&w.lock)
	return w
}

func (w *WriterAtReader) WriteAt(p []byte, off int64) (n int, err error) {
	n, err = w.file.WriteAt(p, off)
	if err != nil {
		return n, errors.Wrap(err, "failed to write data to file")
	}

	w.lock.Lock()
	readyDataRange := append(w.readyDataRange, DataRange{Start: off, End: off + int64(n) - 1}) //nolint:gocritic
	slices.SortFunc(readyDataRange, func(a, b DataRange) int {
		return int(a.Start - b.Start)
	})

	// Merge overlapping or adjacent ranges
	merged := make([]DataRange, 0)
	if len(readyDataRange) > 0 {
		current := readyDataRange[0]
		for i := 1; i < len(readyDataRange); i++ {
			if current.End+1 >= readyDataRange[i].Start {
				if readyDataRange[i].End > current.End {
					current.End = readyDataRange[i].End
				}
			} else {
				merged = append(merged, current)
				current = readyDataRange[i]
			}
		}
		merged = append(merged, current)
	}

	w.readyDataRange = merged
	w.eofReached = w.isEOF()
	w.cond.Broadcast()
	w.lock.Unlock()

	return n, nil
}

func (w *WriterAtReader) isEOF() bool {
	return len(w.readyDataRange) == 1 &&
		w.readyDataRange[0].Start == 0 &&
		w.readyDataRange[0].End == w.size-1
}

func (w *WriterAtReader) dataReady(offset, size int64) bool {
	for _, r := range w.readyDataRange {
		if r.Start <= offset && r.End >= offset+size-1 {
			return true
		}
	}
	return false
}

func (w *WriterAtReader) Read(p []byte) (int, error) {
	w.lock.Lock()
	for !w.eofReached && !w.dataReady(w.curOffset, int64(len(p))) {
		w.cond.Wait()
	}

	if w.eofReached && w.curOffset >= w.size {
		w.lock.Unlock()
		return 0, io.EOF
	}

	readSize := int64(len(p))
	if w.curOffset+readSize > w.size {
		readSize = w.size - w.curOffset
	}

	w.lock.Unlock()

	n, err := w.file.ReadAt(p[:readSize], w.curOffset)
	w.curOffset += int64(n)

	if err != nil && !errors.Is(err, io.EOF) {
		return n, errors.Wrap(err, "failed to read data from file")
	}
	return n, nil
}

func (w *WriterAtReader) Close() error {
	w.lock.Lock()
	w.eofReached = true
	w.cond.Broadcast()
	w.lock.Unlock()
	return nil
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

	size, err := o.getObjectSize(ctx, bucketName, layerKey)
	if err != nil {
		return errors.Wrap(err, "failed to get object size")
	}

	coreNumber := runtime.NumCPU()

	baseDir := filepath.Dir(downloadedFilePath)
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return errors.Wrap(err, "failed to create base dir for downloaded file")
	}

	_, err = os.OpenFile(downloadedFilePath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return errors.Wrap(err, "failed to create downloaded file")
	}

	file, err := os.OpenFile(downloadedFilePath, os.O_RDWR, 0644)
	if err != nil {
		return errors.Wrap(err, "failed to reopen file with O_DIRECT")
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

	w := NewWriterAtReader(file, size)

	startTime := time.Now()
	logger.Info("downloading and streaming decompression layer from S3...")
	errCh := make(chan error, 1)

	go func() {
		defer func() {
			_ = w.Close()
		}()
		start := time.Now()
		err := parrallelDownload(ctx, presignedURL, w, size, coreNumber)
		if err != nil {
			errCh <- errors.Wrap(err, "failed to download layer from S3")
		}
		duration := time.Since(start)
		seconds := duration.Seconds()
		if seconds != 0 {
			bandwidth := float64(size) / 1024 / 1024 / seconds
			logger.WithField("bandwidth", fmt.Sprintf("%.2f MB/s", bandwidth)).WithField("duration", duration).WithField("parallel", coreNumber).WithField("size", size).Info("download finished")
		} else {
			logger.WithField("bandwidth", "unknown MB/s").WithField("duration", duration).WithField("parallel", coreNumber).WithField("size", size).Info("download finished")
		}
	}()

	go func() {
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, "sh", "-c", "pzstd -d | tar -xf -")
		cmd.Stderr = &stderr
		cmd.Stdin = w
		cmd.Dir = tempName

		err = cmd.Run()
		if err != nil {
			err = errors.Wrapf(err, "failed to run command: %s, stderr: %s", stringifyCmd(cmd), stderr.String())
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	err = <-errCh
	if err != nil {
		return errors.Wrap(err, "failed to download layer from S3")
	}

	duration := time.Since(startTime)

	logger.WithField("duration", duration).Info("the layer has been downloaded and decompressed")

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

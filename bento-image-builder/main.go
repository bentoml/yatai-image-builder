package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pkg/errors"

	"github.com/klauspost/compress/zstd"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"github.com/jessevdk/go-flags"

	"github.com/bentoml/yatai-image-builder/common"
	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/seekabletar"

	"encoding/json"
	"time"

	"github.com/containerd/stargz-snapshotter/estargz"
	"github.com/containerd/stargz-snapshotter/estargz/zstdchunked"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/regclient/regclient"
	regclientconfig "github.com/regclient/regclient/config"
	"github.com/regclient/regclient/types/descriptor"
	"github.com/regclient/regclient/types/manifest"
	"github.com/regclient/regclient/types/ref"
)

type CtxKey string

const (
	loggerCtxKey           CtxKey = "logger"
	defaultStargzChunkSize        = 32 * 1024 * 1024 // 32MB
)

type ComressionType string

const (
	CompressionTypeZSTD ComressionType = "zstd"
	CompressionTypeNone ComressionType = "none"
)

var (
	_logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
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

func L(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerCtxKey).(*slog.Logger); ok {
		return logger
	}
	return _logger
}

func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey, logger)
}

type buildOptions struct {
	DockerfilePath        string            `short:"f" long:"dockerfile" description:"Path to the dockerfile" required:"true"`
	ContextPath           string            `short:"c" long:"context" description:"Path to the context directory" required:"true"`
	Output                string            `short:"o" long:"output" description:"Path to the output file" required:"false"`
	BuildArg              map[string]string `long:"build-arg" description:"Build arg" required:"false"`
	S3Bucket              string            `long:"s3-bucket" description:"S3 bucket name" required:"false"`
	ImageName             string            `long:"image-name" description:"Name of the image to push" required:"false"`
	ImageRegistryInsecure bool              `long:"image-registry-insecure" description:"Insecure registry" required:"false"`
	EnableStargz          bool              `long:"enable-stargz" description:"Enable stargz" required:"false"`
	Force                 bool              `long:"force" description:"Force" required:"false"`
	StargzChunkSize       int               `long:"stargz-chunk-size" description:"Stargz chunk size" required:"false"`
}

// OCIConfig represents the image configuration
type OCIConfig struct {
	MediaType    string            `json:"mediaType"`
	Architecture string            `json:"architecture"`
	OS           string            `json:"os"`
	RootFS       RootFS            `json:"rootfs"`
	Config       Config            `json:"config"`
	Created      string            `json:"created"`
	Labels       map[string]string `json:"labels,omitempty"`
}

type RootFS struct {
	Type    string   `json:"type"`
	DiffIDs []string `json:"diff_ids"` // nolint:tagliatelle
}

type Config struct {
	Env          []string            `json:"Env,omitempty"`          // nolint:tagliatelle
	Cmd          []string            `json:"Cmd,omitempty"`          // nolint:tagliatelle
	WorkingDir   string              `json:"WorkingDir,omitempty"`   // nolint:tagliatelle
	Labels       map[string]string   `json:"Labels,omitempty"`       // nolint:tagliatelle
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"` // nolint:tagliatelle
}

type Compression interface {
	estargz.Compressor
	estargz.Decompressor

	// DecompressTOC decompresses the passed blob and returns a reader of TOC JSON.
	// This is needed to be used from metadata pkg
	DecompressTOC(io.Reader) (tocJSON io.ReadCloser, err error)
}

type CompressionFactory func() Compression

type zstdCompression struct {
	*zstdchunked.Compressor
	*zstdchunked.Decompressor
}

func ZstdCompressionWithLevel(compressionLevel zstd.EncoderLevel) CompressionFactory {
	return func() Compression {
		return &zstdCompression{&zstdchunked.Compressor{CompressionLevel: compressionLevel}, &zstdchunked.Decompressor{}}
	}
}

type streamingCompressAndUploadOptions struct {
	bucketName      string
	objectKey       string
	reader          io.Reader
	enableStargz    bool
	stargzChunkSize int
	compression     ComressionType
}

func streamingCompressAndUpload(ctx context.Context, opts streamingCompressAndUploadOptions) error {
	bucketName := opts.bucketName
	objectKey := opts.objectKey
	reader := opts.reader
	enableStargz := opts.enableStargz
	stargzChunkSize := opts.stargzChunkSize
	compression := opts.compression

	logger := L(ctx).With(slog.String("bucket", bucketName), slog.String("object-key", objectKey))

	var compressedReader io.Reader

	// Start compression and upload in a goroutine
	compressionErrCh := make(chan error, 1)
	if enableStargz {
		logger.InfoContext(ctx, "stargz enabled")
		// Create a temporary file to store the tar content
		tmpFile, err := os.CreateTemp("", "bento-tar-*")
		if err != nil {
			return errors.Wrap(err, "failed to create temporary file")
		}
		defer os.Remove(tmpFile.Name())
		defer tmpFile.Close()

		// Copy tar content to temporary file
		_, err = io.Copy(tmpFile, reader)
		if err != nil {
			return errors.Wrap(err, "failed to copy tar to temporary file")
		}

		// Seek back to start of file
		if _, err := tmpFile.Seek(0, 0); err != nil {
			return errors.Wrap(err, "failed to seek temporary file")
		}

		logger.InfoContext(ctx, "building stargz...")
		// Get file info for size
		fi, err := tmpFile.Stat()
		if err != nil {
			return errors.Wrap(err, "failed to get temporary file info")
		}

		sr := io.NewSectionReader(tmpFile, 0, fi.Size())
		args := []estargz.Option{
			estargz.WithChunkSize(stargzChunkSize),
		}
		if compression == CompressionTypeZSTD {
			args = append(args, estargz.WithCompression(ZstdCompressionWithLevel(zstd.SpeedFastest)()))
		}
		blob, err := estargz.Build(sr, args...)
		if err != nil {
			return errors.Wrap(err, "failed to build stargz")
		}
		compressedReader = blob
		compressionErrCh <- nil
	} else {
		if compression == CompressionTypeZSTD {
			pr, pw := io.Pipe()
			compressedReader = pr
			go func() {
				defer pw.Close()

				enc, err := zstd.NewWriter(pw, zstd.WithEncoderLevel(zstd.SpeedFastest))
				if err != nil {
					compressionErrCh <- errors.Wrap(err, "failed to create zstd encoder")
					return
				}
				defer enc.Close()

				logger.InfoContext(ctx, "compressing...")
				_, err = io.Copy(enc, reader)
				if err != nil {
					compressionErrCh <- errors.Wrap(err, "failed to copy and compress")
					return
				}
				logger.InfoContext(ctx, "compressed")
				compressionErrCh <- nil
			}()
		} else {
			tempFile, err := os.CreateTemp("", "bento-seekable-tar-*")
			if err != nil {
				return errors.Wrap(err, "failed to create temp file")
			}
			defer os.Remove(tempFile.Name())

			_, err = io.Copy(tempFile, reader)
			if err != nil {
				return errors.Wrap(err, "failed to copy to temp file")
			}
			err = tempFile.Close()
			if err != nil {
				return errors.Wrap(err, "failed to close temp file")
			}

			tempFile, err = os.Open(tempFile.Name())
			if err != nil {
				return errors.Wrap(err, "failed to open temp file")
			}

			compressedReader, err = seekabletar.ConvertToSeekableTar(tempFile)
			if err != nil {
				return errors.Wrap(err, "failed to generate seekable tar")
			}
			compressionErrCh <- nil
		}
	}

	return uploadToS3(ctx, bucketName, objectKey, compressedReader, compressionErrCh)
}

func checkS3ObjectExists(ctx context.Context, bucketName, objectKey string) (bool, error) {
	logger := L(ctx).With(slog.String("bucket", bucketName), slog.String("object-key", objectKey))

	s5cmdPath, err := exec.LookPath("s5cmd")
	if err != nil {
		return false, errors.Wrap(err, "s5cmd not found in PATH")
	}

	baseArgs := []string{}

	s3EndpointURL := os.Getenv("S3_ENDPOINT_URL")
	if s3EndpointURL != "" {
		logger.InfoContext(ctx, "using S3 endpoint URL", slog.String("url", s3EndpointURL))
		baseArgs = append(baseArgs, "--endpoint-url", s3EndpointURL)
		logger = logger.With(slog.String("endpoint-url", s3EndpointURL))
	}

	logger.InfoContext(ctx, "checking if object exists...")
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s5cmdPath, append(baseArgs, "ls", fmt.Sprintf("s3://%s/%s", bucketName, objectKey))...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if !strings.Contains(stderrStr, "no object found") {
			logger.ErrorContext(ctx, "failed to check if object exists", slog.String("stderr", stderrStr))
			return false, errors.Wrap(err, "failed to check if object exists")
		}
		logger.InfoContext(ctx, "object does not exist")
		return false, nil
	} else {
		logger.InfoContext(ctx, "object exists")
		return true, nil
	}
}

func uploadToS3(ctx context.Context, bucketName, objectKey string, reader io.Reader, compressionErrCh chan error) error {
	logger := L(ctx).With(slog.String("bucket", bucketName), slog.String("object-key", objectKey))

	// Create s5cmd command
	s5cmdPath, err := exec.LookPath("s5cmd")
	if err != nil {
		return errors.Wrap(err, "s5cmd not found in PATH")
	}

	baseArgs := []string{}

	s3EndpointURL := os.Getenv("S3_ENDPOINT_URL")
	if s3EndpointURL != "" {
		logger.InfoContext(ctx, "using S3 endpoint URL", slog.String("url", s3EndpointURL))
		baseArgs = append(baseArgs, "--endpoint-url", s3EndpointURL)
	}

	s3Uri := fmt.Sprintf("s3://%s/%s", bucketName, objectKey)

	cmd := exec.CommandContext(ctx, s5cmdPath, append(baseArgs, "pipe", s3Uri)...)
	cmd.Stdin = reader
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Run s5cmd
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, "failed to upload to S3 using s5cmd")
	}

	// Wait for compression to complete
	if err := <-compressionErrCh; err != nil {
		return err
	}

	return nil
}

func createTarReader(srcDir string) io.ReadCloser {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()

		tw := tar.NewWriter(pw)
		defer tw.Close()

		err := filepath.Walk(srcDir, func(file string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			relPath, err := filepath.Rel(srcDir, file)
			if err != nil {
				return errors.Wrap(err, "failed to get relative path")
			}

			tarPath := relPath

			if fi.Mode().IsDir() {
				if file != srcDir {
					hdr, err := tar.FileInfoHeader(fi, "")
					if err != nil {
						return errors.Wrap(err, "failed to get tar header")
					}
					hdr.Name = tarPath + "/"
					if err := tw.WriteHeader(hdr); err != nil {
						return errors.Wrap(err, "failed to write tar header")
					}
				}
				return nil
			}

			f, err := os.Open(file)
			if err != nil {
				return errors.Wrap(err, "failed to open file")
			}
			defer f.Close()

			hdr, err := tar.FileInfoHeader(fi, "")
			if err != nil {
				return errors.Wrap(err, "failed to get tar header")
			}
			hdr.Name = tarPath

			if err := tw.WriteHeader(hdr); err != nil {
				return errors.Wrap(err, "failed to write tar header")
			}

			if _, err := io.Copy(tw, f); err != nil {
				return errors.Wrap(err, "failed to copy file")
			}

			return nil
		})

		if err != nil {
			pw.CloseWithError(err)
			return
		}
	}()

	return pr
}

type getLayerDescOptions struct {
	digestStr    string
	s3BucketName string
	objectKey    string
	baseImage    string
	isBentoLayer bool
	enableStargz bool
	compression  ComressionType
}

func getLayerDesc(opts getLayerDescOptions) (descriptor.Descriptor, ocispecv1.Descriptor) {
	digestStr := opts.digestStr
	s3BucketName := opts.s3BucketName
	objectKey := opts.objectKey
	baseImage := opts.baseImage
	isBentoLayer := opts.isBentoLayer
	enableStargz := opts.enableStargz
	compression := opts.compression

	format := common.DescriptorAnnotationValueFormatTar
	if enableStargz {
		format = common.DescriptorAnnotationValueFormatStargz
	}
	layerDesc := descriptor.Descriptor{
		MediaType: "application/vnd.oci.image.layer.v1.tar+zstd",
		Size:      0,                        // We don't know the exact size
		Digest:    digest.Digest(digestStr), // placeholder
		Annotations: map[string]string{
			"org.opencontainers.image.source":       fmt.Sprintf("s3://%s/%s", s3BucketName, objectKey),
			common.DescriptorAnnotationBucket:       s3BucketName,
			common.DescriptorAnnotationObjectKey:    objectKey,
			common.DescriptorAnnotationCompression:  string(compression),
			common.DescriptorAnnotationBaseImage:    baseImage,
			common.DescriptorAnnotationIsBentoLayer: strconv.FormatBool(isBentoLayer),
			common.DescriptorAnnotationFormat:       format,
		},
	}

	if enableStargz {
		layerDesc.Annotations["containerd.io/snapshot/remote/stargz.reference"] = fmt.Sprintf("dummy.io/%s/%s", s3BucketName, objectKey)
		layerDesc.Annotations["containerd.io/snapshot/remote/stargz.digest"] = digestStr
	}

	ociLayerDesc := ocispecv1.Descriptor{
		MediaType:   layerDesc.MediaType,
		Size:        layerDesc.Size,
		Digest:      layerDesc.Digest,
		Annotations: layerDesc.Annotations,
	}

	return layerDesc, ociLayerDesc
}

const (
	normalLayerObjectKeyPrefix = "layers/"
	stargzLayerObjectKeyPrefix = "stargz-layers/"
)

func chownRecursive(ctx context.Context, root string, uid, gid int) error {
	return errors.Wrap(filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return errors.Wrap(err, "failed to walk the directory")
		}
		// Change ownership of the file/directory
		err = os.Chown(path, uid, gid)
		if err != nil {
			L(ctx).ErrorContext(ctx, "failed to chown the file/directory", slog.String("path", path), slog.String("error", err.Error()))
			return errors.Wrap(err, "failed to chown the file/directory")
		}
		return nil
	}), "failed to walk the directory")
}

func build(ctx context.Context, opts buildOptions) error {
	stargzChunkSize := defaultStargzChunkSize
	if opts.StargzChunkSize > 0 {
		stargzChunkSize = opts.StargzChunkSize
	}

	objectKeyPrefix := normalLayerObjectKeyPrefix
	if opts.EnableStargz {
		objectKeyPrefix = stargzLayerObjectKeyPrefix
	}

	logger := L(ctx).With(slog.String("context-path", opts.ContextPath)).With(slog.String("bucket", opts.S3Bucket)).With(slog.String("image", opts.ImageName))
	ctx = WithLogger(ctx, logger)

	bentoLayerObjectKeyCh := make(chan string, 1)
	bentoLayerUploadErrCh := make(chan error, 1)

	logger.InfoContext(ctx, "preparing bento files...")
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "bento-layer-*")
	if err != nil {
		logger.ErrorContext(ctx, "failed to create temporary directory", slog.String("error", err.Error()))
		return errors.Wrap(err, "failed to create temporary directory")
	}
	defer os.RemoveAll(tmpDir)

	tmpBentoDir := filepath.Join(tmpDir, "home/bentoml/bento")
	err = os.MkdirAll(tmpBentoDir, 0755)
	if err != nil {
		logger.ErrorContext(ctx, "failed to create temporary directory", slog.String("error", err.Error()))
		return errors.Wrap(err, "failed to create temporary directory")
	}

	logger.InfoContext(ctx, "copying files to temporary directory...", slog.String("path", tmpBentoDir))
	// Copy all files to temporary directory
	cmd := exec.CommandContext(ctx, "cp", "-a", opts.ContextPath+"/.", tmpBentoDir) // nolint:gosec
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		logger.ErrorContext(ctx, "failed to copy files to temporary directory", slog.String("error", err.Error()))
		return errors.Wrapf(err, "failed to copy files to temporary directory, stderr: %s", stderr.String())
	}

	// Change ownership of temporary directory
	logger.InfoContext(ctx, "chown -R 1034:1034", slog.String("path", filepath.Dir(tmpBentoDir)))
	err = chownRecursive(ctx, filepath.Dir(tmpBentoDir), 1034, 1034)
	if err != nil {
		logger.ErrorContext(ctx, "failed to chown temporary directory", slog.String("error", err.Error()))
		return errors.Wrap(err, "failed to chown temporary directory")
	}
	logger.InfoContext(ctx, "chown done")

	logger.InfoContext(ctx, "chmod a+x env/docker/entrypoint.sh")
	err = os.Chmod(filepath.Join(tmpBentoDir, "env/docker/entrypoint.sh"), 0755)
	if err != nil {
		err = errors.Wrap(err, "failed to chmod +x env/docker/entrypoint.sh")
		return err
	}

	bentoHash, err := common.HashFile(tmpBentoDir)
	logger.InfoContext(ctx, "bento hash", slog.String("hash", bentoHash))
	if err != nil {
		err = errors.Wrap(err, "failed to get hash of file")
		return err
	}

	bentoLayerObjectKey := objectKeyPrefix + bentoHash

	bentoLayerExists, err := checkS3ObjectExists(ctx, opts.S3Bucket, bentoLayerObjectKey)
	if err != nil {
		return errors.Wrap(err, "failed to check if object exists")
	}

	if !bentoLayerExists || opts.Force {
		go func() {
			logger := logger.With(slog.String("object-key", bentoLayerObjectKey))
			logger.InfoContext(ctx, "bento layer does not exist, building bento layer...")
			logger.InfoContext(ctx, "compressing and streaming upload of bento layer to S3...")
			bentoTarReader := createTarReader(tmpDir)
			defer bentoTarReader.Close()
			err = streamingCompressAndUpload(ctx, streamingCompressAndUploadOptions{
				bucketName:      opts.S3Bucket,
				objectKey:       bentoLayerObjectKey,
				reader:          bentoTarReader,
				enableStargz:    opts.EnableStargz,
				stargzChunkSize: stargzChunkSize,
				compression:     CompressionTypeNone,
			})
			if err != nil {
				logger.ErrorContext(ctx, "failed to upload bento layer", slog.String("error", err.Error()))
				bentoLayerUploadErrCh <- err
			}
			bentoLayerObjectKeyCh <- bentoLayerObjectKey
			logger.InfoContext(ctx, "bento layer has been uploaded")
		}()
	} else {
		logger := logger.With(slog.String("object-key", bentoLayerObjectKey))
		logger.InfoContext(ctx, "bento layer exists, skipping build and upload")
		bentoLayerObjectKeyCh <- bentoLayerObjectKey
	}

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

	baseLayerObjectKey := objectKeyPrefix + imageInfo.Hash

	logger.InfoContext(ctx, "checking if base layer exists...", slog.String("object-key", baseLayerObjectKey))

	baseLayerExists, err := checkS3ObjectExists(ctx, opts.S3Bucket, baseLayerObjectKey)
	if err != nil {
		return errors.Wrap(err, "failed to check if object exists")
	}

	if !baseLayerExists || opts.Force {
		logger := logger.With(slog.String("object-key", baseLayerObjectKey))

		logger.InfoContext(ctx, "base layer does not exist, building base layer...")

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

		cmd := []string{"bash", "-c", strings.Join(append([]string{"set -ex", "mkdir -p " + workDir}, imageInfo.Commands...), ";\n")}

		logger.InfoContext(ctx, "creating container...", slog.String("image", imageInfo.BaseImage), slog.String("context", opts.ContextPath), slog.Any("cmd", cmd), slog.Any("env", imageInfo.Env), slog.String("working_dir", opts.ContextPath))

		_ = cli.ContainerRemove(ctx, imageInfo.Hash, container.RemoveOptions{
			Force: true,
		})

		resp, err := cli.ContainerCreate(ctx, &container.Config{
			Image:      imageInfo.BaseImage,
			Cmd:        cmd,
			Env:        append(imageInfo.Env, "CONTEXT="+opts.ContextPath),
			WorkingDir: workDir,
			Tty:        true,
			OpenStdin:  true,
		}, hostConfig, nil, nil, imageInfo.Hash)
		if err != nil {
			err = errors.Wrap(err, "failed to create container")
			return err
		}

		logger.InfoContext(ctx, "container created", slog.String("image", imageInfo.BaseImage), slog.String("context", opts.ContextPath), slog.Any("cmd", cmd), slog.Any("env", imageInfo.Env), slog.String("working_dir", opts.ContextPath))

		defer func() {
			logger := logger.With(slog.String("container-id", resp.ID))
			err = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{
				Force: true,
			})
			if err != nil {
				logger.ErrorContext(ctx, "failed to remove container", slog.String("error", err.Error()))
			} else {
				logger.InfoContext(ctx, "container removed")
			}
		}()

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

		logger.InfoContext(ctx, "waiting for container to exit...")

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

		logger.InfoContext(ctx, "container has exited")

		logger.InfoContext(ctx, "exporting and streaming upload of the base layer to S3...")
		exportOut, err := cli.ContainerExport(ctx, resp.ID)
		if err != nil {
			err = errors.Wrap(err, "failed to export container")
			return err
		}
		defer exportOut.Close()

		err = streamingCompressAndUpload(ctx, streamingCompressAndUploadOptions{
			bucketName:      opts.S3Bucket,
			objectKey:       baseLayerObjectKey,
			reader:          exportOut,
			enableStargz:    opts.EnableStargz,
			stargzChunkSize: stargzChunkSize,
			compression:     CompressionTypeNone,
		})
		if err != nil {
			return err
		}

		logger.InfoContext(ctx, "the base layer has been uploaded")
	} else {
		logger.InfoContext(ctx, "base layer exists, skipping build and upload")
	}

	// Push to registry if registry options are provided
	if opts.ImageName != "" {
		logger.InfoContext(ctx, "pushing image to image registry...")

		registry, _, _ := strings.Cut(opts.ImageName, "/")

		exHostLocal := regclientconfig.Host{
			Name: registry,
		}
		if opts.ImageRegistryInsecure {
			logger.WarnContext(ctx, "using insecure registry")
			exHostLocal.TLS = regclientconfig.TLSDisabled
		}

		// Create registry client
		rc := regclient.New(
			regclient.WithConfigHost(exHostLocal),
			regclient.WithDockerCerts(),
			regclient.WithDockerCreds(),
		)

		// Create image reference
		imgRef, err := ref.New(opts.ImageName)
		if err != nil {
			return errors.Wrap(err, "failed to create image reference")
		}

		baseLayerPlaceholderDigest := string(digest.FromBytes([]byte(baseLayerObjectKey)))
		bentoLayerPlaceholderDigest := string(digest.FromBytes([]byte(bentoLayerObjectKey)))

		var bentoLayerObjectKey string
		select {
		case <-ctx.Done():
			return errors.Wrap(ctx.Err(), "context canceled")
		case err := <-bentoLayerUploadErrCh:
			return err
		case bentoLayerObjectKey_ := <-bentoLayerObjectKeyCh:
			bentoLayerObjectKey = bentoLayerObjectKey_
		}

		// Create image config
		config := OCIConfig{
			MediaType:    "application/vnd.oci.image.config.v1+json",
			Architecture: "amd64",
			OS:           "linux",
			Created:      time.Now().UTC().Format(time.RFC3339),
			Config: Config{
				Env:        imageInfo.Env,
				WorkingDir: imageInfo.WorkingDir,
			},
			RootFS: RootFS{
				Type: "layers",
				DiffIDs: []string{
					baseLayerPlaceholderDigest,
					bentoLayerPlaceholderDigest,
				},
			},
			Labels: map[string]string{
				"org.opencontainers.image.source": fmt.Sprintf("s3://%s/%s", opts.S3Bucket, bentoLayerObjectKey),
			},
		}

		// Convert config to JSON
		configJSON, err := json.Marshal(config)
		if err != nil {
			return errors.Wrap(err, "failed to marshal config")
		}

		// Calculate config digest
		configDigest := digest.FromBytes(configJSON)

		configDesc := descriptor.Descriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Size:      int64(len(configJSON)),
			Digest:    configDigest,
		}

		ociConfigDesc := ocispecv1.Descriptor{
			MediaType: configDesc.MediaType,
			Size:      configDesc.Size,
			Digest:    configDesc.Digest,
		}

		baseLayerDesc, ociBaseLayerDesc := getLayerDesc(getLayerDescOptions{
			digestStr:    baseLayerPlaceholderDigest,
			s3BucketName: opts.S3Bucket,
			objectKey:    baseLayerObjectKey,
			baseImage:    imageInfo.BaseImage,
			isBentoLayer: false,
			enableStargz: opts.EnableStargz,
			compression:  CompressionTypeNone,
		})
		bentoLayerDesc, ociBentoLayerDesc := getLayerDesc(getLayerDescOptions{
			digestStr:    bentoLayerPlaceholderDigest,
			s3BucketName: opts.S3Bucket,
			objectKey:    bentoLayerObjectKey,
			baseImage:    imageInfo.BaseImage,
			isBentoLayer: true,
			enableStargz: opts.EnableStargz,
			compression:  CompressionTypeNone,
		})

		// Create manifest
		manifest_ := ocispecv1.Manifest{
			Versioned: ocispec.Versioned{
				SchemaVersion: 2,
			},
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Config:    ociConfigDesc,
			Layers:    []ocispecv1.Descriptor{ociBaseLayerDesc, ociBentoLayerDesc},
		}

		manifestBytes, err := json.Marshal(manifest_)
		if err != nil {
			return errors.Wrap(err, "failed to marshal manifest")
		}

		manifestObj, err := manifest.New(manifest.WithRaw(manifestBytes))
		if err != nil {
			return errors.Wrap(err, "failed to create manifest")
		}

		configJSONReader := bytes.NewReader(configJSON)

		// Push config
		logger.InfoContext(ctx, "pushing config to image registry...")
		_, err = rc.BlobPut(ctx, imgRef, configDesc, configJSONReader)
		if err != nil {
			return errors.Wrap(err, "failed to push config to image registry")
		}
		logger.InfoContext(ctx, "config has been pushed to image registry")

		// Push base layer
		baseLayerReader := bytes.NewReader([]byte(baseLayerObjectKey))
		logger.InfoContext(ctx, "pushing base layer to image registry...")
		_, err = rc.BlobPut(ctx, imgRef, baseLayerDesc, baseLayerReader)
		if err != nil {
			return errors.Wrap(err, "failed to push base layer to image registry")
		}
		logger.InfoContext(ctx, "base layer has been pushed to image registry")

		bentoLayerReader := bytes.NewReader([]byte(bentoLayerObjectKey))
		logger.InfoContext(ctx, "pushing bento layer to image registry...")
		_, err = rc.BlobPut(ctx, imgRef, bentoLayerDesc, bentoLayerReader)
		if err != nil {
			return errors.Wrap(err, "failed to push bento layer to image registry")
		}
		logger.InfoContext(ctx, "bento layer has been pushed to image registry")

		logger.InfoContext(ctx, "pushing image metadata to image registry...")
		err = rc.ManifestPut(ctx, imgRef, manifestObj)
		if err != nil {
			return errors.Wrap(err, "failed to push manifest to image registry")
		}
		logger.InfoContext(ctx, "image metadata has been pushed to image registry")

		logger.InfoContext(ctx, "successfully pushed image to image registry")
	} else {
		logger.InfoContext(ctx, "base layer exists, skipping upload")
	}

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
		L(ctx).ErrorContext(ctx, "failed to build image", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

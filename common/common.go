package common

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asottile/dockerfile"
	"github.com/pkg/errors"
	"github.com/zeebo/blake3"
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

func HashDir(dirPath string) (string, error) {
	hasher := blake3.New()

	var filePaths []string
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			filePaths = append(filePaths, path)
		}
		return nil
	})

	if err != nil {
		return "", errors.Wrap(err, "failed to walk directory")
	}

	sort.Strings(filePaths)

	for _, path := range filePaths {
		fileHash, err := HashFile(path)
		if err != nil {
			return "", err
		}

		_, _ = hasher.Write([]byte(path))
		_, _ = hasher.Write([]byte(fileHash))
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func HashFile(filePath string) (string, error) {
	stat, err := os.Stat(filePath)
	if err != nil {
		return "", errors.Wrap(err, "failed to stat file")
	}
	if stat.IsDir() {
		return HashDir(filePath)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", errors.Wrap(err, "failed to open file")
	}
	defer file.Close()

	hasher := blake3.New()

	// Add file mode (permissions)
	_, _ = hasher.Write([]byte(fmt.Sprintf("%d", stat.Mode()))) //nolint: staticcheck

	if _, err := io.Copy(hasher, file); err != nil {
		return "", errors.Wrap(err, "failed to copy file")
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type ImageInfo struct {
	BaseImage  string            `json:"base_image"` // nolint:tagliatelle
	Commands   []string          `json:"commands"`
	Env        []string          `json:"env"`
	Args       map[string]string `json:"args"`
	Hash       string            `json:"hash"`
	WorkingDir string            `json:"working_dir"` // nolint:tagliatelle
}

func GetImageInfo(ctx context.Context, dockerfileContent string, contextPath string, buildArgs map[string]string) (*ImageInfo, error) {
	reader := strings.NewReader(dockerfileContent)
	dockerfileCommands, err := dockerfile.ParseReader(reader)
	if err != nil {
		logger.ErrorContext(ctx, "failed to parse dockerfile", slog.String("error", err.Error()))
		os.Exit(1)
	}

	originalFileHashes := make([]string, 0)

	baseImage := ""
	commands := make([]string, 0)
	env := make([]string, 0)
	args := make(map[string]string)
	workingDir := ""

	for _, dockerfileCommand := range dockerfileCommands {
		if dockerfileCommand.Cmd == "FROM" {
			baseImage = dockerfileCommand.Value[0]
		}
		if dockerfileCommand.Cmd == "RUN" {
			cmd := dockerfileCommand.Value[0]
			if cmd == "chmod +x /home/bentoml/bento/env/docker/entrypoint.sh" {
				continue
			}
			cmd = strings.TrimSuffix(cmd, "; exit 0")
			commands = append(commands, cmd)
		}
		if dockerfileCommand.Cmd == "ARG" {
			k, v, _ := strings.Cut(dockerfileCommand.Value[0], "=")
			if suppliedValue, exists := buildArgs[k]; exists {
				v = suppliedValue
			}
			args[k] = v
			commands = append(commands, fmt.Sprintf("export %s=%s", k, v))
		}
		if dockerfileCommand.Cmd == "ENV" {
			k, v := dockerfileCommand.Value[0], dockerfileCommand.Value[1]
			if strings.HasPrefix(v, "$") {
				v = args[strings.TrimPrefix(v, "$")]
			}
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		if dockerfileCommand.Cmd == "WORKDIR" {
			workingDir = dockerfileCommand.Value[0]
			if strings.HasPrefix(workingDir, "$") {
				workingDir = args[strings.TrimPrefix(workingDir, "$")]
			}
			commands = append(commands, "cd "+workingDir)
		}
		if dockerfileCommand.Cmd == "COPY" {
			orginalFilePath := dockerfileCommand.Value[0]
			targetFilePath := dockerfileCommand.Value[1]
			if orginalFilePath == "." {
				continue
			}
			contextPathJoined := false
			if orginalFilePath[0] != '/' {
				orginalFilePath = filepath.Join(contextPath, orginalFilePath)
				contextPathJoined = true
			}
			hashStr, err := HashFile(orginalFilePath)
			if err != nil {
				logger.ErrorContext(ctx, "failed to get hash of file", slog.String("error", err.Error()))
				return nil, err
			}
			originalFileHashes = append(originalFileHashes, hashStr)
			targetFileDir := filepath.Dir(targetFilePath)
			if strings.HasSuffix(targetFilePath, "/") {
				targetFileDir = filepath.Dir(targetFileDir)
			}
			if targetFileDir != "" && targetFileDir != "." {
				commands = append(commands, "mkdir -p "+targetFileDir)
			}
			if contextPathJoined {
				orginalFilePath = strings.Replace(orginalFilePath, contextPath, "${CONTEXT}", 1)
			}
			commands = append(commands, fmt.Sprintf("cp -a %s %s", orginalFilePath, targetFilePath))
			if dockerfileCommand.Flags[0] == "--chown=bentoml:bentoml" {
				commands = append(commands, "chown -R bentoml:bentoml "+targetFilePath)
			}
		}
	}

	hashBaseStr := baseImage + "__" + strings.Join(originalFileHashes, ":") + "__" + strings.Join(commands, ":") + "__" + strings.Join(env, ":")

	hasher := blake3.New()
	_, _ = hasher.Write([]byte(hashBaseStr))
	hashStr := hex.EncodeToString(hasher.Sum(nil))

	return &ImageInfo{
		BaseImage:  baseImage,
		Commands:   commands,
		Env:        env,
		Args:       args,
		Hash:       hashStr,
		WorkingDir: workingDir,
	}, nil
}

func GetContainerImageS3EndpointURL() string {
	return os.Getenv("CONTAINER_IMAGE_S3_ENDPOINT_URL")
}

func GetContainerImageS3Bucket() string {
	return os.Getenv("CONTAINER_IMAGE_S3_BUCKET")
}

func GetContainerImageS3EnableStargz() bool {
	return os.Getenv("CONTAINER_IMAGE_S3_ENABLE_STARGZ") == "true"
}

func GetContainerImageS3AccessKeyID() string {
	return os.Getenv("CONTAINER_IMAGE_S3_ACCESS_KEY_ID")
}

func GetContainerImageS3SecretAccessKey() string {
	return os.Getenv("CONTAINER_IMAGE_S3_SECRET_ACCESS_KEY")
}

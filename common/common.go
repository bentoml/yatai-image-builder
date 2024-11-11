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

	"crypto/md5" // nolint:gosec

	"github.com/asottile/dockerfile"
	"github.com/pkg/errors"
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

func md5Dir(path string) (string, error) {
	files, err := os.ReadDir(path)
	if err != nil {
		return "", errors.Wrap(err, "failed to read directory")
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})

	hash := md5.New() // nolint:gosec
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		filePath := filepath.Join(path, file.Name())
		fileMd5, err := md5File(filePath)
		if err != nil {
			return "", err
		}
		hash.Write([]byte(fileMd5))
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func md5File(path string) (string, error) {
	// if is directory, then hash the directory content
	stat, err := os.Stat(path)
	if err != nil {
		return "", errors.Wrap(err, "failed to stat file")
	}
	if stat.IsDir() {
		return md5Dir(path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.Wrap(err, "failed to open file")
	}
	defer file.Close()

	hash := md5.New() // nolint:gosec
	if _, err := io.Copy(hash, file); err != nil {
		return "", errors.Wrap(err, "failed to copy file")
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

type ImageInfo struct {
	BaseImage string            `json:"base_image"` // nolint:tagliatelle
	Commands  []string          `json:"commands"`
	Env       []string          `json:"env"`
	Args      map[string]string `json:"args"`
	Hash      string            `json:"hash"`
}

func GetImageInfo(ctx context.Context, dockerfileContent string, contextPath string, buildArgs map[string]string) (*ImageInfo, error) {
	reader := strings.NewReader(dockerfileContent)
	dockerfileCommands, err := dockerfile.ParseReader(reader)
	if err != nil {
		logger.ErrorContext(ctx, "failed to parse dockerfile", slog.String("error", err.Error()))
		os.Exit(1)
	}

	originalFileMd5s := make([]string, 0)

	baseImage := ""
	commands := make([]string, 0)
	env := make([]string, 0)
	args := make(map[string]string)

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
			commands = append(commands, fmt.Sprintf("cd %s", dockerfileCommand.Value[0]))
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
			md5Str, err := md5File(orginalFilePath)
			if err != nil {
				logger.ErrorContext(ctx, "failed to get md5 of file", slog.String("error", err.Error()))
				return nil, err
			}
			originalFileMd5s = append(originalFileMd5s, md5Str)
			targetFileDir := filepath.Dir(targetFilePath)
			if strings.HasSuffix(targetFilePath, "/") {
				targetFileDir = filepath.Dir(targetFileDir)
			}
			if targetFileDir != "" && targetFileDir != "." {
				commands = append(commands, fmt.Sprintf("mkdir -p %s", targetFileDir))
			}
			if contextPathJoined {
				orginalFilePath = strings.Replace(orginalFilePath, contextPath, "${CONTEXT}", 1)
			}
			commands = append(commands, fmt.Sprintf("cp -a %s %s", orginalFilePath, targetFilePath))
			if dockerfileCommand.Flags[0] == "--chown=bentoml:bentoml" {
				commands = append(commands, fmt.Sprintf("chown -R bentoml:bentoml %s", targetFilePath))
			}
		}
	}

	hashBaseStr := baseImage + "__" + strings.Join(originalFileMd5s, ":") + "__" + strings.Join(commands, ":") + "__" + strings.Join(env, ":")

	// nolint: gosec
	hasher := md5.New()
	hasher.Write([]byte(hashBaseStr))
	hashStr := hex.EncodeToString(hasher.Sum(nil))

	return &ImageInfo{
		BaseImage: baseImage,
		Commands:  commands,
		Env:       env,
		Args:      args,
		Hash:      hashStr,
	}, nil
}

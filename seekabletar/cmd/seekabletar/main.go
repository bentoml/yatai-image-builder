package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/bentoml/yatai-image-builder/seekabletar/pkg/seekabletar"
)

const usage = `
Usage: seekabletar generate|toc|seek <tar-file-path> [dest-path]
`

func main() {
	if len(os.Args) < 3 {
		fmt.Print(usage)
		os.Exit(1)
	}

	op := os.Args[1]
	tarFilePath := os.Args[2]
	tarFile, err := os.Open(tarFilePath)
	if err != nil {
		panic(err)
	}
	defer func() {
		_ = tarFile.Close()
	}()

	if op != "generate" && op != "toc" && op != "seek" {
		fmt.Print(usage)
		os.Exit(1) //nolint:gocritic
	}

	switch op {
	case "toc":
		toc, err := seekabletar.CalculateTOCFromTar(tarFile)
		if err != nil {
			panic(err)
		}

		r, err := json.Marshal(toc)
		if err != nil {
			panic(err)
		}

		fmt.Println(string(r))
	case "generate":
		destPath := os.Args[3]
		if destPath == "" {
			fmt.Print(usage)
			os.Exit(1)
		}

		seekableTar, err := seekabletar.GenerateSeekableTar(tarFile)
		if err != nil {
			panic(err)
		}

		file, err := os.Create(destPath)
		if err != nil {
			panic(err)
		}
		defer file.Close()

		_, err = io.Copy(file, seekableTar)
		if err != nil {
			panic(err)
		}
	case "seek":
		toc, err := seekabletar.GetTOCFromSeekableTar(tarFile)
		if err != nil {
			panic(err)
		}
		r, err := json.Marshal(toc)
		if err != nil {
			panic(err)
		}
		fmt.Println(string(r))
	}
}

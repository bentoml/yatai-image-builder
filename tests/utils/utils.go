//nolint:wrapcheck
package utils

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2" //nolint:golint,revive,stylecheck,staticcheck
)

// Run executes the provided command within this context
func Run(cmd *exec.Cmd) ([]byte, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir
	fmt.Fprintf(GinkgoWriter, "running dir: %s\n", cmd.Dir)

	// To allow make commands be executed from the project directory which is subdir on SDK repo
	// TODO:(user) You might not need the following code
	if err := os.Chdir(cmd.Dir); err != nil {
		fmt.Fprintf(GinkgoWriter, "chdir dir: %s\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed with error: (%w) %s", command, err, string(output))
	}

	return output, nil
}

func RunAsDaemon(cmd *exec.Cmd) (*os.Process, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir
	fmt.Fprintf(GinkgoWriter, "running dir: %s\n", cmd.Dir)

	// To allow make commands be executed from the project directory which is subdir on SDK repo
	// TODO:(user) You might not need the following code
	if err := os.Chdir(cmd.Dir); err != nil {
		fmt.Fprintf(GinkgoWriter, "chdir dir: %s\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.ExtraFiles = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command := strings.Join(cmd.Args, " ")
	fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	err := cmd.Start()
	if err != nil {
		return nil, fmt.Errorf("%s failed with error: (%w)", command, err)
	}

	return cmd.Process, nil
}

// LoadImageToKindCluster loads a local docker image to the kind cluster
func LoadImageToKindClusterWithName(name string) error {
	cluster := "kind"
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		cluster = v
	}
	kindOptions := []string{"load", "docker-image", name, "--name", cluster}
	cmd := exec.Command("kind", kindOptions...)
	_, err := Run(cmd)
	return err
}

// GetNonEmptyLines converts given command output string into individual objects
// according to line breakers, and ignores the empty elements in it.
func GetNonEmptyLines(output string) []string {
	var res []string
	elements := strings.Split(output, "\n")
	for _, element := range elements {
		if element != "" {
			res = append(res, element)
		}
	}

	return res
}

// GetProjectDir will return the directory where the project is
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, err
	}
	wd = strings.ReplaceAll(wd, "/tests/e2e", "")
	return wd, nil
}

// ReplaceInFile replaces all instances of old with new in the file at path.
func ReplaceInFile(path, old, new string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	// false positive
	// nolint:gosec
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.Contains(string(b), old) {
		return errors.New("unable to find the content to be replaced")
	}
	s := strings.ReplaceAll(string(b), old, new)
	err = os.WriteFile(path, []byte(s), info.Mode())
	if err != nil {
		return err
	}
	return nil
}

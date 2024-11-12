package envspec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

type EnvironmentSpec struct {
	BaseImage      string
	PythonVersion  string
	Commands       []string
	PythonPackages []string
}

func (spec EnvironmentSpec) LayerName() string {
	layerName := spec.BaseImage

	if spec.PythonVersion != "" {
		layerName += fmt.Sprintf("-python%s", spec.PythonVersion)
	}

	if len(spec.Commands) > 0 {
		// Sort commands to ensure consistency
		sortedCommands := append([]string{}, spec.Commands...)
		sort.Strings(sortedCommands)

		// Calculate hash of commands
		commandsHash := sha256.Sum256([]byte(strings.Join(sortedCommands, ",")))
		layerName += fmt.Sprintf("-commands-%s", hex.EncodeToString(commandsHash[:8]))
	}

	if len(spec.PythonPackages) > 0 {
		// Sort packages to ensure consistency
		sortedPackages := append([]string{}, spec.PythonPackages...)
		sort.Strings(sortedPackages)

		// Calculate hash of packages
		packagesHash := sha256.Sum256([]byte(strings.Join(sortedPackages, ",")))
		layerName += fmt.Sprintf("-packages-%s", hex.EncodeToString(packagesHash[:8]))
	}

	return layerName
}

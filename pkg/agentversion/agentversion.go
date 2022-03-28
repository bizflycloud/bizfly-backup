package agentversion

import (
	"fmt"
)

var (
	CurrentVersion string
	GitCommit      string
	BuildTime      string
)

// Version returns agent version.
func Version() string {
	if CurrentVersion == "" {
		CurrentVersion = "dev"
	}

	return fmt.Sprintf("version: %s, commit: %s, build: %s", CurrentVersion, GitCommit, BuildTime)
}

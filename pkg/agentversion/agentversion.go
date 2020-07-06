package agentversion

import (
	"fmt"
)

var (
	version   string
	commit    string
	buildTime string
)

// Version returns agent version.
func Version() string {
	if version == "" {
		version = "dev"
	}

	return fmt.Sprintf("version: %s, commit: %s, built: %s", version, commit, buildTime)
}

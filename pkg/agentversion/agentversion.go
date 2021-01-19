package agentversion

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

	return version
}

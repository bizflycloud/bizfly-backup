//go:build linux
// +build linux

package support

import "os/user"

func CheckPath() (string, string, string, string, error) {
	var logErrorPath, logDebugPath, logInfoPath, cachePath string
	user, err := user.Current()
	if err != nil {
		return "", "", "", "", err
	}

	if user.Username == "root" {
		logErrorPath = "/var/log/bizfly-backup/error.log"
		logDebugPath = "/var/log/bizfly-backup/debug.log"
		logInfoPath = "/var/log/bizfly-backup/info.log"

		cachePath = "/var/lib/bizfly-backup/.cache"
	} else {
		logErrorPath = user.HomeDir + "/var/log/bizfly-backup/error.log"
		logDebugPath = user.HomeDir + "/var/log/bizfly-backup/debug.log"
		logInfoPath = user.HomeDir + "/var/log/bizfly-backup/info.log"

		cachePath = user.HomeDir + "/var/lib/bizfly-backup/.cache"
	}

	return logErrorPath, logDebugPath, logInfoPath, cachePath, nil
}

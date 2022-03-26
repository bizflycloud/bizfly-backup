//go:build linux
// +build linux

package support

import "os/user"

func CheckPath() (string, string, error) {
	var logPath, cachePath string
	currentUser, err := user.Current()
	if err != nil {
		return "", "", err
	}

	if currentUser.Username == "root" {
		logPath = "/var/log/bizfly-backup/bizfly-backup.log"
		cachePath = "/var/lib/bizfly-backup/.cache"
	} else {
		logPath = currentUser.HomeDir + "/var/log/bizfly-backup/bizfly-backup.log"
		cachePath = ".cache"
	}

	return logPath, cachePath, nil
}

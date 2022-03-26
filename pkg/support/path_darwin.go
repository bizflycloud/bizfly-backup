//go:build darwin
// +build darwin

package support

import "os/user"

func CheckPath() (string, string, error) {
	var logPath, cachePath string
	user, err := user.Current()
	if err != nil {
		return "", "", err
	}

	if user.Username == "root" {
		logPath = "/var/log/bizfly-backup/bizfly-backup.log"
		cachePath = "/var/lib/bizfly-backup/.cache"
	} else {
		logPath = user.HomeDir + "/var/log/bizfly-backup/bizfly-backup.log"
		cachePath = ".cache"
	}

	return logPath, cachePath, nil
}

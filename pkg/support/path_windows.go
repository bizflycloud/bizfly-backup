package support

import "os/user"

const (
	LOG_ERROR_PATH = "C:\\Program Files\\bizfly-backup\\log\\error.log"
	LOG_DEBUG_PATH = "C:\\Program Files\\bizfly-backup\\log\\debug.log"
	LOG_INFO_PATH  = "C:\\Program Files\\bizfly-backup\\log\\info.log"

	CACHE_PATH = "C:\\Program Files\\bizfly-backup\\lib\\.cache"
)

func CheckPath() (string, string, string, string, error) {
	var logErrorPath, logDebugPath, logInfoPath, cachePath string
	user, err := user.Current()
	if err != nil {
		return "", "", "", "", err
	}

	if strings.Contains(user.Username, "Administrator") {
		logErrorPath = "C:\\Program Files\\bizfly-backup\\log\\error.log"
		logDebugPath = "C:\\Program Files\\bizfly-backup\\log\\debug.log"
		logInfoPath = "C:\\Program Files\\bizfly-backup\\log\\info.log"

		cachePath = "C:\\Program Files\\bizfly-backup\\lib\\.cache"
	} else {
		logErrorPath = user.HomeDir + "/var/log/bizfly-backup/error.log"
		logDebugPath = user.HomeDir + "/var/log/bizfly-backup/debug.log"
		logInfoPath = user.HomeDir + "/var/log/bizfly-backup/info.log"

		cachePath = ".cache"
	}

	return logErrorPath, logDebugPath, logInfoPath, cachePath, nil
}

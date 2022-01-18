package support

func CheckPath() (string, string, string, string, error) {
	logErrorPath := "C:\\Program Files\\bizfly-backup\\log\\error.log"
	logDebugPath := "C:\\Program Files\\bizfly-backup\\log\\debug.log"
	logInfoPath := "C:\\Program Files\\bizfly-backup\\log\\info.log"

	cachePath := "C:\\Program Files\\bizfly-backup\\lib\\.cache"

	return logErrorPath, logDebugPath, logInfoPath, cachePath, nil
}

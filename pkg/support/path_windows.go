package support

func CheckPath() (string, string, error) {
	logPath := "C:\\Program Files\\bizfly-backup\\log\\bizfly-backup.log"
	cachePath := "C:\\Program Files\\bizfly-backup\\lib\\.cache"

	return logPath, cachePath, nil
}

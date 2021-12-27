//go:build linux
// +build linux

package support

const (
	LOG_ERROR_PATH = "/var/log/bizfly-backup/error.log"
	LOG_DEBUG_PATH = "/var/log/bizfly-backup/debug.log"
	LOG_INFO_PATH  = "/var/log/bizfly-backup/info.log"

	CACHE_PATH = "/var/lib/bizfly-backup/.cache"
)

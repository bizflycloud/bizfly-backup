package backupapi

import (
	"io/fs"
	"syscall"
	"time"
)

func ItemLocal(fi fs.FileInfo) (time.Time, time.Time, time.Time, uint32, uint32) {
	var atimeLocal, ctimeLocal, mtimeLocal time.Time
	if stat, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		atimeLocal = time.Unix(0, stat.LastAccessTime.Nanoseconds()).UTC()
		ctimeLocal = time.Unix(0, stat.LastAccessTime.Nanoseconds()).UTC()
		mtimeLocal = time.Unix(0, stat.LastAccessTime.Nanoseconds()).UTC()
	}
	uid := 0
	gid := 0

	return atimeLocal, ctimeLocal, mtimeLocal, uid, gid
}

package support

import (
	"io/fs"
	"syscall"
	"time"
)

func ItemLocal(fi fs.FileInfo) (time.Time, time.Time, time.Time, uint32, uint32, uint32) {
	var atimeLocal, ctimeLocal, mtimeLocal time.Time
	var size uint32
	if stat, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		atimeLocal = time.Unix(0, stat.LastAccessTime.Nanoseconds()).UTC()
		ctimeLocal = time.Unix(0, stat.LastWriteTime.Nanoseconds()).UTC()
		mtimeLocal = time.Unix(0, stat.LastWriteTime.Nanoseconds()).UTC()
		size = stat.FileSizeHigh
	}
	uid := uint32(0)
	gid := uint32(0)

	return atimeLocal, ctimeLocal, mtimeLocal, uid, gid, size
}

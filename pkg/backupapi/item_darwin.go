// +build darwin

package backupapi

import (
	"io/fs"
	"syscall"
	"time"
)

func ItemLocal(fi fs.FileInfo) (time.Time, time.Time, time.Time, uint32, uint32) {
	var atimeLocal, ctimeLocal, mtimeLocal time.Time
	var uid, gid uint32
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		atimeLocal = time.Unix(stat.Atimespec.Unix()).UTC()
		ctimeLocal = time.Unix(stat.ctimespec.Unix()).UTC()
		mtimeLocal = time.Unix(stat.Mtimespec.Unix()).UTC()
		uid = stat.Uid
		gid = stat.Gid
	}
	return atimeLocal, ctimeLocal, mtimeLocal, uid, gid
}

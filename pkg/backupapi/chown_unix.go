// +build !windows

package backupapi

import (
	"os"
)

func SetChownItem(name string, uid int, gid int) error {
	err := os.Chown(name, uid, gid)
	if err != nil {
		return err
	}
	return nil
}

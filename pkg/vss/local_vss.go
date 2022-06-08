package vss

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// ErrorHandler is used to report errors via callback
type ErrorHandler func(item string, err error) error

// MessageHandler is used to report errors/messages via callbacks.
type MessageHandler func(msg string, args ...interface{})

// LocalVss is a wrapper around the local file system which uses windows volume
// shadow copy service (VSS) in a transparent way.
type LocalVss struct {
	snapshots       map[string]VssSnapshot
	failedSnapshots map[string]struct{}
	mutex           sync.RWMutex
	msgError        ErrorHandler
	msgMessage      MessageHandler
}

// NewLocalVss creates a new wrapper around the windows filesystem using volume
// shadow copy service to access locked files.
func NewLocalVss(msgError ErrorHandler, msgMessage MessageHandler) *LocalVss {
	return &LocalVss{
		snapshots:       make(map[string]VssSnapshot),
		failedSnapshots: make(map[string]struct{}),
		msgError:        msgError,
		msgMessage:      msgMessage,
	}
}

// DeleteSnapshots deletes all snapshots that were created automatically.
func (vss *LocalVss) DeleteSnapshots() {
	vss.mutex.Lock()
	defer vss.mutex.Unlock()

	activeSnapshots := make(map[string]VssSnapshot)

	for volumeName, snapshot := range vss.snapshots {
		if err := snapshot.Delete(); err != nil {
			_ = vss.msgError(volumeName, fmt.Errorf("failed to delete VSS snapshot: %s", err))
			activeSnapshots[volumeName] = snapshot
		}
	}

	vss.snapshots = activeSnapshots
}

// fixpath returns an absolute path on windows, so restic can open long file
// names.
func fixpath(name string) string {
	abspath, err := filepath.Abs(name)
	if err == nil {
		// Check if \\?\UNC\ already exist
		if strings.HasPrefix(abspath, `\\?\UNC\`) {
			return abspath
		}
		// Check if \\?\ already exist
		if strings.HasPrefix(abspath, `\\?\`) {
			return abspath
		}
		// Check if path starts with \\
		if strings.HasPrefix(abspath, `\\`) {
			return strings.Replace(abspath, `\\`, `\\?\UNC\`, 1)
		}
		// Normal path
		return `\\?\` + abspath
	}
	return name
}

// HasPathPrefix returns true if p is a subdir of (or a file within) base. It
// assumes a file system which is case sensitive. If the paths are not of the
// same type (one is relative, the other is absolute), false is returned.
func HasPathPrefix(base, p string) bool {
	if filepath.VolumeName(base) != filepath.VolumeName(p) {
		return false
	}

	// handle case when base and p are not of the same type
	if filepath.IsAbs(base) != filepath.IsAbs(p) {
		return false
	}

	base = filepath.Clean(base)
	p = filepath.Clean(p)

	if base == p {
		return true
	}

	for {
		dir := filepath.Dir(p)

		if base == dir {
			return true
		}

		if p == dir {
			break
		}

		p = dir
	}

	return false
}

// SnapshotPath returns the path inside a VSS snapshots if it already exists.
// If the path is not yet available as a snapshot, a snapshot is created.
// If creation of a snapshot fails the file's original path is returned as
// a fallback.
func (vss *LocalVss) SnapshotPath(path string) string {

	fixPath := fixpath(path)

	if strings.HasPrefix(fixPath, `\\?\UNC\`) {
		// UNC network shares are currently not supported so we access the regular file
		// without snapshotting
		// TODO: right now there is a problem in fixpath(): "\\host\share" is not returned as a UNC path
		//       "\\host\share\" is returned as a valid UNC path
		return path
	}

	fixPath = strings.TrimPrefix(fixpath(path), `\\?\`)
	fixPathLower := strings.ToLower(fixPath)
	volumeName := filepath.VolumeName(fixPath)
	volumeNameLower := strings.ToLower(volumeName)

	vss.mutex.RLock()

	// ensure snapshot for volume exists
	_, snapshotExists := vss.snapshots[volumeNameLower]
	_, snapshotFailed := vss.failedSnapshots[volumeNameLower]
	if !snapshotExists && !snapshotFailed {
		vss.mutex.RUnlock()
		vss.mutex.Lock()
		defer vss.mutex.Unlock()

		_, snapshotExists = vss.snapshots[volumeNameLower]
		_, snapshotFailed = vss.failedSnapshots[volumeNameLower]

		if !snapshotExists && !snapshotFailed {
			vssVolume := volumeNameLower + string(filepath.Separator)
			vss.msgMessage("creating VSS snapshot for [%s]\n", vssVolume)

			if snapshot, err := NewVssSnapshot(vssVolume, 120, vss.msgError); err != nil {
				_ = vss.msgError(vssVolume, fmt.Errorf("failed to create snapshot for [%s]: %s", vssVolume, err))
				vss.failedSnapshots[volumeNameLower] = struct{}{}
			} else {
				vss.snapshots[volumeNameLower] = snapshot
				vss.msgMessage("successfully created snapshot for [%s]\n", vssVolume)
				if len(snapshot.mountPointInfo) > 0 {
					vss.msgMessage("mountpoints in snapshot volume [%s]:\n", vssVolume)
					for mp, mpInfo := range snapshot.mountPointInfo {
						info := ""
						if !mpInfo.IsSnapshotted() {
							info = " (not snapshotted)"
						}
						vss.msgMessage(" - %s%s\n", mp, info)
					}
				}
			}
		}
	} else {
		defer vss.mutex.RUnlock()
	}

	var SnapshotPath string
	if snapshot, ok := vss.snapshots[volumeNameLower]; ok {
		// handle case when data is inside mountpoint
		for mountPoint, info := range snapshot.mountPointInfo {
			if HasPathPrefix(mountPoint, fixPathLower) {
				if !info.IsSnapshotted() {
					// requested path is under mount point but mount point is
					// not available as a snapshot (e.g. no filesystem support,
					// removable media, etc.)
					//  -> try to backup without a snapshot
					return path
				}

				// filepath.rel() should always succeed because we checked that fixPath is either
				// the same path or below mountPoint and operation is case-insensitive
				relativeToMount, err := filepath.Rel(mountPoint, fixPath)
				if err != nil {
					panic(err)
				}

				SnapshotPath = filepath.Join(info.GetSnapshotDeviceObject(), relativeToMount)

				if SnapshotPath == info.GetSnapshotDeviceObject() {
					SnapshotPath += string(filepath.Separator)
				}

				return SnapshotPath
			}
		}

		// requested data is directly on the volume, not inside a mount point
		SnapshotPath = filepath.Join(snapshot.GetSnapshotDeviceObject(),
			strings.TrimPrefix(fixPath, volumeName))
		if SnapshotPath == snapshot.GetSnapshotDeviceObject() {
			SnapshotPath = SnapshotPath + string(filepath.Separator)
		}

	} else {
		// no snapshot is available for the requested path:
		//  -> try to backup without a snapshot
		// TODO: log warning?
		SnapshotPath = path
	}

	return SnapshotPath
}

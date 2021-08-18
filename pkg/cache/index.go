package cache

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type Index struct {
	BackupDirectoryID string           `json:"backup_directory_id"`
	RecoveryPointID   string           `json:"recovery_point_id"`
	Items             map[string]*Node `json:"items"`
}

func NewIndex(bdID string, rpID string) *Index {
	return &Index{
		BackupDirectoryID: bdID,
		RecoveryPointID:   rpID,
		Items:             make(map[string]*Node),
	}
}

type ChunkInfo struct {
	Start  uint   `json:"start"`
	Length uint   `json:"length"`
	Etag   string `json:"etag"`
	Data   []byte `json:"-"`
}

type Node struct {
	Name         string       `json:"name"`
	Type         string       `json:"type"`
	Mode         os.FileMode  `json:"mode,omitempty"`
	ModTime      time.Time    `json:"mtime,omitempty"`
	AccessTime   time.Time    `json:"atime,omitempty"`
	ChangeTime   time.Time    `json:"ctime,omitempty"`
	UID          uint32       `json:"uid"`
	GID          uint32       `json:"gid"`
	User         string       `json:"user,omitempty"`
	Group        string       `json:"group,omitempty"`
	Size         uint64       `json:"size,omitempty"`
	LinkTarget   string       `json:"linktarget,omitempty"`
	Content      []*ChunkInfo `json:"content,omitempty"`
	AbsolutePath string       `json:"path"`
	BasePath     string       `json:"base_path"`
	RelativePath string       `json:"relative_path"`
}

func (node *Node) fill_extra(path string, fi os.FileInfo) (err error) {
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}

	node.ChangeTime = time.Unix(stat.Ctim.Unix())
	node.AccessTime = time.Unix(stat.Atim.Unix())
	node.UID = stat.Uid
	node.GID = stat.Gid

	if u, nil := user.LookupId(strconv.Itoa(int(stat.Uid))); err == nil {
		node.User = u.Username
	}

	switch node.Type {
	case "file":
		node.Size = uint64(stat.Size)
	case "dir":
		// nothing to do
	case "symlink":
		node.LinkTarget, err = os.Readlink(path)
	default:
		panic(fmt.Sprintf("invalid node type %q", node.Type))
	}
	return err
}

func NodeFromFileInfo(rootPath string, pathName string, fi os.FileInfo) (*Node, error) {
	rel, err := filepath.Rel(rootPath, pathName)
	if err != nil {
		return nil, err
	}
	base := filepath.Base(rootPath)
	if rel == "." {
		rel = fi.Name()
	} else {
		rel = filepath.Join(base, rel)
	}
	node := &Node{
		Name:         fi.Name(),
		Mode:         fi.Mode() & os.ModePerm,
		ModTime:      fi.ModTime(),
		AbsolutePath: pathName,
		BasePath:     rootPath,
		RelativePath: rel,
	}

	switch fi.Mode() & (os.ModeType | os.ModeCharDevice) {
	case 0:
		node.Type = "file"
	case os.ModeDir:
		node.Type = "dir"
	case os.ModeSymlink:
		node.Type = "symlink"
	}

	err = node.fill_extra(pathName, fi)
	return node, err
}

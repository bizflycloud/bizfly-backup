package cache

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bizflycloud/bizfly-backup/pkg/support"
)

type Index struct {
	BackupDirectoryID string           `json:"backup_directory_id"`
	RecoveryPointID   string           `json:"recovery_point_id"`
	Items             map[string]*Node `json:"items"`
	TotalFiles        int64            `json:"total_files"`
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
}

type Node struct {
	Name         string       `json:"name"`
	Type         string       `json:"type"`
	Sha256Hash   Sha256Hash   `json:"sha256_hash,omitempty"`
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

type Sha256Hash []byte

func (h Sha256Hash) String() string {
	return hex.EncodeToString(h[:])
}

func (h Sha256Hash) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.String())
}

func (h *Sha256Hash) UnmarshalJSON(b []byte) error {
	var s string
	err := json.Unmarshal(b, &s)
	if err != nil {
		return err
	}
	*h = make([]byte, len(s)/2)
	_, err = hex.Decode(*h, []byte(s))
	if err != nil {
		return err
	}

	return nil
}

func (node *Node) fill_extra(path string, fi os.FileInfo) (err error) {
	atime, ctime, _, uid, gid, size := support.ItemLocal(fi)
	node.ChangeTime = ctime
	node.AccessTime = atime
	node.UID = uid
	node.GID = gid

	if u, nil := user.LookupId(strconv.Itoa(int(uid))); err == nil {
		node.User = u.Username
	}

	switch node.Type {
	case "file":
		node.Size = uint64(size)
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

package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/bizflycloud/bizfly-backup/pkg/support"
)

const (
	dirMode  = 0700
	tempPath = "tmp"
)

type Repository struct {
	path string
	mcID string
	rpID string
}

type Type int

const (
	INDEX = iota
	CHUNK
)

func (t Type) String() string {
	switch t {
	case INDEX:
		return "index.json"
	case CHUNK:
		return "chunk.json"
	}

	return fmt.Sprintf("unknown type %d", t)
}

// NewDirRepository creates a new dir-baked repository at the given path.
func NewRepository(path string, mcID string, rpID string) (*Repository, error) {
	d := &Repository{
		path: path,
		mcID: mcID,
		rpID: rpID,
	}

	err := d.create()
	if err != nil {
		return nil, err
	}

	return d, nil
}

func (r *Repository) create() error {
	dirs := []string{
		r.path,
		path.Join(r.path, r.mcID, r.rpID, tempPath),
	}

	for _, dir := range dirs {
		err := os.MkdirAll(dir, dirMode)
		if err != nil {
			return err
		}
	}

	return nil
}

// Return temp directory in correct directory for this repository.
func (r *Repository) tempFile() (*os.File, error) {
	return ioutil.TempFile(path.Join(r.path, r.mcID, r.rpID, tempPath), "temp-")
}

// Rename temp file to final name according to type and ID.
func (r *Repository) renameFile(file *os.File, t Type) error {
	filename := r.filename(t)
	return os.Rename(file.Name(), filename)
}

// Construct path for given Type and ID.
func (r *Repository) filename(t Type) string {
	return path.Join(r.path, r.mcID, r.rpID, t.String())
}

func (r *Repository) SaveIndex(index *Index) error {
	buf, err := json.Marshal(index)
	if err != nil {
		return err
	}
	f, err := r.tempFile()
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(buf)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	err = r.renameFile(f, INDEX)
	if err != nil {
		return err
	}
	return nil
}

func (r *Repository) SaveChunk(chunk *Chunk) error {
	buf, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	f, err := r.tempFile()
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(buf)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	err = r.renameFile(f, CHUNK)
	if err != nil {
		return err
	}
	return nil
}

// listCacheDirs returns the list of cache directories.
func listCacheDirs(cacheDir string) ([]os.FileInfo, error) {
	f, err := os.Open(cacheDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, err
	}

	entries, err := f.Readdir(-1)
	if err != nil {
		return nil, err
	}

	err = f.Close()
	if err != nil {
		return nil, err
	}

	result := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		result = append(result, entry)
	}

	return result, nil
}

// olderThan returns the list of cache directories older than max.
func olderThan(cacheDir string, maxCacheAge time.Duration) ([]os.FileInfo, error) {
	entries, err := listCacheDirs(cacheDir)
	if err != nil {
		return nil, err
	}

	var oldCacheDirs []os.FileInfo
	for _, fi := range entries {
		_, _, mtime, _, _, _ := support.ItemLocal(fi)
		if !isOld(mtime, maxCacheAge) {
			continue
		}
		oldCacheDirs = append(oldCacheDirs, fi)
	}

	return oldCacheDirs, nil
}

// old returns a list of cache directories with a modification time
func old(basedir string, maxCacheAge time.Duration) ([]os.FileInfo, error) {
	return olderThan(basedir, maxCacheAge)
}

// isOld returns true if the timestamp is considered old.
func isOld(t time.Time, maxCacheAge time.Duration) bool {
	oldest := time.Now().Add(-maxCacheAge)
	return t.Before(oldest)
}

// RemoveOldCache remove old cache after max time exists
func RemoveOldCache(maxCacheAge time.Duration) error {
	oldCacheDirs, err := old(support.CACHE_PATH, maxCacheAge)
	if err != nil {
		return err
	}
	if len(oldCacheDirs) != 0 {
		for _, item := range oldCacheDirs {
			dir := filepath.Join(support.CACHE_PATH, item.Name())
			err := os.RemoveAll(dir)
			if err != nil {
				return err
			}
			fmt.Printf("removing old cache dirs %s \n", dir)
		}
	}
	return nil
}

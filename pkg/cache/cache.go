package cache

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path"
)

const (
	dirMode  = 0700
	tempPath = "tmp"
)

type Repository struct {
	path string
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

	panic(fmt.Sprintf("unknown type %d", t))
}

// NewDirRepository creates a new dir-baked repository at the given path.
func NewRepository(path string, rpID string) (*Repository, error) {
	d := &Repository{
		path: path,
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
		path.Join(r.path, r.rpID, tempPath),
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
	return ioutil.TempFile(path.Join(r.path, r.rpID, tempPath), "temp-")
}

// Rename temp file to final name according to type and ID.
func (r *Repository) renameFile(file *os.File, t Type) error {
	filename := r.filename(t)
	return os.Rename(file.Name(), filename)
}

// Construct path for given Type and ID.
func (r *Repository) filename(t Type) string {
	return path.Join(r.path, r.rpID, t.String())
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
	err = r.renameFile(f, CHUNK)
	if err != nil {
		return err
	}
	return nil
}

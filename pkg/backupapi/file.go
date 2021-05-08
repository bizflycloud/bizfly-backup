package backupapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"

	"github.com/bizflycloud/bizfly-backup/pkg/storage"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/restic/chunker"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

const ChunkUploadLowerBound = 15 * 1000 * 1000

// File ...
type File struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int    `json:"size"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	ContentType string `json:"content_type"`
	Etag        string `json:"eTag"`
}

// FileInfo ...
type FileInfo struct {
	ItemName     string `json:"item_name"`
	Size         string `json:"size"`
	LastModified string `json:"last_modified"`
	ItemType     string `json:"item_type"`
	Mode         string `json:"mode"`
}

// Chunk ...
type Chunk struct {
	CreatedAt    string `json:"created_at"`
	Deleted      bool   `json:"deleted"`
	DeleteAt     string `json:"delete_at"`
	ID           string `json:"id"`
	Offset       uint   `json:"offset"`
	Length       uint   `json:"length"`
	HexSha256    string `json:"hex_sha256"`
	PresignedURl string `json:"presigned_url"`
	UpdatedAt    string `json:"updated_at"`
	Uri          string `json:"uri"`
}

func (c *Client) saveFileInfoPath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/file", recoveryPointID)
}

func (c *Client) urlStringFromRelPath(relPath string) (string, error) {
	if c.ServerURL.Path != "" && c.ServerURL.Path != "/" {
		relPath = path.Join(c.ServerURL.Path, relPath)
	}
	relURL, err := url.Parse(relPath)
	if err != nil {
		return "", err
	}

	u := c.ServerURL.ResolveReference(relURL)
	return u.String(), nil
}

func (c *Client) saveFileInfo(recoveryPointID string, fileInfo *FileInfo) (File, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveFileInfoPath(recoveryPointID))
	if err != nil {
		return File{}, err
	}
	req, err := c.NewRequest(http.MethodPost, reqURL, fileInfo)
	if err != nil {
		return File{}, err
	}
	resp, reqErr := c.Do(req)
	if reqErr != nil {
		return File{}, reqErr
	}
	var file File
	if err := json.NewDecoder(resp.Body).Decode(&file); err != nil {
		return File{}, err
	}

	return file, nil
}

func (c *Client) saveListFileInfo(recoveryPointID string, listFileInfo []FileInfo) ([]string, error) {
	listFileID := make([]string, 0)
	for _, fileInfo := range listFileInfo {
		file, err := c.saveFileInfo(recoveryPointID, &fileInfo)
		if err != nil {
			return listFileID, err
		}
		listFileID = append(listFileID, file.ID)
	}

	return listFileID, nil
}

func (c *Client) saveChunk(recoveryPointID string, fileID string, chunkInfo *Chunk) (Chunk, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveChunkPath(recoveryPointID, fileID))
	if err != nil {
		return Chunk{}, err
	}

	req, err := c.NewRequest(http.MethodPost, reqURL, chunkInfo)
	if err != nil {
		return Chunk{}, err
	}
	resp, reqErr := c.Do(req)
	if reqErr != nil {
		return Chunk{}, reqErr
	}
	var chunk Chunk
	if err := json.NewDecoder(resp.Body).Decode(&chunk); err != nil {
		return Chunk{}, err
	}

	return chunk, nil
}

func (c *Client) uploadFile(fn string, backupDir string) error {
	var backend storage.Backend

	listFileInfo, listFile := WalkerDir(backupDir)
	listFileID, err := c.saveListFileInfo(fn, listFileInfo)
	if err != nil {
		return err
	}

	for _, fileID := range listFileID {
		for _, singleFile := range listFile {
			file, err := os.Open(singleFile)
			if err != nil {
				return err
			}

			chk := chunker.New(file, 0x3dea92648f6e83)
			buf := make([]byte, ChunkUploadLowerBound)

			for {
				chunk, err := chk.Next(buf)
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				hash := sha256.Sum256(chunk.Data)
				keyObject := hex.EncodeToString(hash[:])

				_, errSave := c.saveChunk(fn, fileID, &Chunk{
					Offset:    chunk.Start,
					Length:    chunk.Length,
					HexSha256: keyObject,
				})
				if errSave != nil {
					return errSave
				}

				exist, _ := backend.HeadObject(keyObject)
				if !exist {
					err := backend.PutObject(keyObject, chunk.Data)
					if err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}

func (c *Client) UploadFilePresignedUrl(fn string, backupDir string) error {
	sem := semaphore.NewWeighted(int64(runtime.NumCPU()))
	group, ctx := errgroup.WithContext(context.Background())

	listFileInfo, listFile := WalkerDir(backupDir)
	listFileID, err := c.saveListFileInfo(fn, listFileInfo)
	if err != nil {
		return err
	}

	for _, fileID := range listFileID {
		for _, singleFile := range listFile {
			file, err := os.Open(singleFile)
			if err != nil {
				return err
			}

			chk := chunker.New(file, 0x3dea92648f6e83)
			buf := make([]byte, ChunkUploadLowerBound)

			for {
				chunk, err := chk.Next(buf)
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				hash := sha256.Sum256(chunk.Data)
				keyObject := hex.EncodeToString(hash[:])

				object, errSave := c.saveChunk(fn, fileID, &Chunk{
					Offset:    chunk.Start,
					Length:    chunk.Length,
					HexSha256: keyObject,
				})
				if errSave != nil {
					return errSave
				}

				errAcquire := sem.Acquire(ctx, 1)
				if errAcquire != nil {
					log.Printf("acquire err = %+v\n", err)
					continue
				}
				buffTemp := chunk.Data

				group.Go(func() error {
					defer sem.Release(1)
					req, err := http.NewRequest(http.MethodPut, object.PresignedURl, bytes.NewReader(buffTemp))
					if err != nil {
						return err
					}
					retryClient := retryablehttp.NewClient()
					retryClient.RetryMax = 100
					resp, err := c.do(retryClient.StandardClient(), req, "application/json")
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					return nil
				})
			}
			if err := group.Wait(); err != nil {
				return err
			}
		}
	}

	return nil
}

// UploadFile uploads given file to server.
func (c *Client) UploadFile(fn string, r io.Reader, pw io.Writer, batch bool) error {
	if batch {
		return c.uploadMultipart(fn, r, pw)

	}
	return c.uploadFile(fn, r, pw)
}

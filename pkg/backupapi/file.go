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
	"path/filepath"
	"runtime"
	"strconv"
	"time"

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

func (c *Client) UploadFile(fn string, backupDir string) error {
	var backend storage.Backend

	listFileInfo, listFile := walkerDir(backupDir)
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

	listFileInfo, listFile := walkerDir(backupDir)
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

func (c *Client) getChunks(recoveryPointID string, fileID string) ([]Chunk, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveChunkPath(recoveryPointID, fileID))
	if err != nil {
		return nil, err
	}
	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	var chunks []Chunk
	if err := json.NewDecoder(resp.Body).Decode(&chunks); err != nil {
		return nil, err
	}

	return chunks, err
}

func (c *Client) DownloadFile(recoveryPointID string) error {
	var backend storage.Backend
	reqURL, err := c.urlStringFromRelPath(c.fileDownloadPath(recoveryPointID))
	if err != nil {
		return err
	}
	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		log.Println("request error", err)
	}
	resp, _ := c.Do(req)
	var files []File

	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		log.Println("decode error", err)
	}

	for _, f := range files {
		file, err := os.Create(recoveryPointID)
		if err != nil {
			log.Println(err)
		}
		chunks, err := c.getChunks(recoveryPointID, f.ID)
		if err != nil {
			log.Println(err)
		}
		for _, chunk := range chunks {
			data, err := backend.GetObject(chunk.HexSha256)
			if err != nil {
				log.Println("download chunk error", err, chunk.HexSha256)
			}
			_, _ = file.WriteAt(data, int64(chunk.Offset))
		}
	}

	return nil
}

func walkerDir(src string) ([]FileInfo, []string) {
	var listFileInfo []FileInfo
	listFile := make([]string, 0)
	currentTime := time.Now()

	err := filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			fileInfo, _ := os.Lstat(path)
			singleFile := FileInfo{
				ItemName:     fileInfo.Name(),
				Size:         strconv.FormatInt(fileInfo.Size(), 10),
				LastModified: currentTime.Format("2006-01-02 15:04:05.000000"),
				ItemType:     "FILE",
				Mode:         fileInfo.Mode().Perm().String(),
			}
			listFileInfo = append(listFileInfo, singleFile)
			listFile = append(listFile, path)
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	return listFileInfo, listFile
}

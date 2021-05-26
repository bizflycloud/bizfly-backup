package backupapi

import (
	"context"
	"crypto/md5"
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
	"strings"
	"time"

	"github.com/bizflycloud/bizfly-backup/pkg/volume"
	"github.com/restic/chunker"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

const ChunkUploadLowerBound = 15 * 1000 * 1000

// FileInfo ...
type FileInfo struct {
	ItemName     string `json:"item_name"`
	Size         int64  `json:"size"`
	ItemType     string `json:"item_type"`
	Mode         string `json:"mode"`
	LastModified string `json:"last_modified"`
}

// FileInfoRequest ...
type FileInfoRequest struct {
	Files []FileInfo `json:"files"`
}

// File ...
type File struct {
	ContentType  string `json:"content_type"`
	CreatedAt    string `json:"created_at"`
	Etag         string `json:"etag"`
	ID           string `json:"id"`
	ItemName     string `json:"item_name"`
	ItemType     string `json:"item_type"`
	LastModified string `json:"last_modified"`
	Mode         string `json:"mode"`
	RealName     string `json:"real_name"`
	Size         int    `json:"size"`
	Status       string `json:"status"`
	UpdatedAt    string `json:"updated_at"`
	DeletedAt    string `json:"deleted_at"`
	Deleted      bool   `json:"deleted"`
	IsDir        bool   `json:"is_dir"`
}

// FileResponse
type FilesResponse []File

// RecoveryPointResponse
type RecoveryPointResponse struct {
	Files []File `json:"files"`
	Total int    `json:"total"`
}

// ChunkRequest
type ChunkRequest struct {
	Length uint   `json:"length"`
	Offset uint   `json:"offset"`
	Etag   string `json:"etag"`
}

// ChunkResponse
type ChunkResponse struct {
	ID           string `json:"id"`
	Offset       uint   `json:"offset"`
	Length       uint   `json:"length"`
	Etag         string `json:"etag"`
	Uri          string `json:"uri"`
	DeletedAt    string `json:"deleted_at"`
	Deleted      bool   `json:"deleted"`
	PresignedURL struct {
		Head string `json:"head"`
		Put  string `json:"put"`
	} `json:"presigned_url"`
}

// InfoDownload
type InfoDownload struct {
	Get    string `json:"get"`
	Offset int    `json:"offset"`
}

// FileDownloadResponse
type FileDownloadResponse struct {
	Info []InfoDownload `json:"info"`
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

func (c *Client) SaveFilesInfo(recoveryPointID string, dir string) (FilesResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveFileInfoPath(recoveryPointID))
	if err != nil {
		return FilesResponse{}, err
	}
	filesInfo, err := WalkerDir(dir)
	if err != nil {
		return FilesResponse{}, err
	}
	req, err := c.NewRequest(http.MethodPost, reqURL, filesInfo.Files)
	if err != nil {
		return FilesResponse{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return FilesResponse{}, err
	}

	defer resp.Body.Close()

	var files FilesResponse
	if err = json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, err
	}

	return files, nil
}

func (c *Client) saveChunk(recoveryPointID string, fileID string, chunk ChunkRequest) (ChunkResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveChunkPath(recoveryPointID, fileID))
	if err != nil {
		return ChunkResponse{}, err
	}

	req, err := c.NewRequest(http.MethodPost, reqURL, chunk)
	if err != nil {
		return ChunkResponse{}, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return ChunkResponse{}, err
	}
	defer resp.Body.Close()

	var chunkResp ChunkResponse

	if err := json.NewDecoder(resp.Body).Decode(&chunkResp); err != nil {
		return ChunkResponse{}, err
	}

	return chunkResp, nil
}

func (c *Client) UploadFile(recoveryPointID string, backupDir string, fi File, volume volume.StorageVolume) error {
	file, err := os.Open(filepath.Join(backupDir, fi.RealName))
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

		hash := md5.Sum(chunk.Data)
		key := hex.EncodeToString(hash[:])
		chunkReq := ChunkRequest{
			Length: chunk.Length,
			Offset: chunk.Start,
			Etag:   key,
		}
		time.Sleep(500 * time.Millisecond)

		chunkResp, err := c.saveChunk(recoveryPointID, fi.ID, chunkReq)
		if err != nil {
			return err
		}

		if chunkResp.PresignedURL.Head != "" {
			key = chunkResp.PresignedURL.Head
		}

		resp, err := volume.HeadObject(key)
		if err != nil {
			return err
		}

		if etagHead, ok := resp.Header["Etag"]; ok {
			integrity := strings.Contains(etagHead[0], chunkResp.Etag)
			if !integrity {
				key = chunkResp.PresignedURL.Put
				_, err := volume.PutObject(key, chunk.Data)
				if err != nil {
					return err
				}
			}
		} else {
			key = chunkResp.PresignedURL.Put
			_, err := volume.PutObject(key, chunk.Data)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (c *Client) RestoreFile(recoveryPointID string, destDir string, volume volume.StorageVolume, restoreSessionKey string, createdAt string) error {
	sem := semaphore.NewWeighted(int64(runtime.NumCPU()))
	group, ctx := errgroup.WithContext(context.Background())

	rp, err := c.GetListFilePath(recoveryPointID)
	if err != nil {
		return err
	}

	for _, f := range rp.Files {
		infos, err := c.GetInfoFileDownload(recoveryPointID, f.ID, restoreSessionKey, createdAt)
		if err != nil {
			return err
		}
		if len(infos.Info) == 0 {
			break
		}

		relativePathRealName := strings.Join(strings.Split(f.RealName, "/")[0:len(strings.Split(f.RealName, "/"))-1], "/")
		absolutePathRealName := filepath.Join(destDir, relativePathRealName)
		fileResore := filepath.Join(absolutePathRealName, filepath.Base(f.RealName))

		if err := EnsureDir(absolutePathRealName); err != nil {
			return err
		}

		file, err := CreateFile(fileResore)
		if err != nil {
			return err
		}
		defer file.Close()

		for _, info := range infos.Info {
			errAcquire := sem.Acquire(ctx, 1)
			if errAcquire != nil {
				continue
			}
			offset := info.Offset
			key := info.Get

			group.Go(func() error {
				defer sem.Release(1)
				data, err := volume.GetObject(key)
				if err != nil {
					return err
				}
				_, errWriteFile := file.WriteAt(data, int64(offset))
				if errWriteFile != nil {
					return nil
				}
				return nil
			})
		}

	}
	if err := group.Wait(); err != nil {
		return err
	}

	return nil
}

func (c *Client) GetListFilePath(recoveryPointID string) (RecoveryPointResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.getListFilePath(recoveryPointID))
	if err != nil {
		return RecoveryPointResponse{}, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return RecoveryPointResponse{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return RecoveryPointResponse{}, err
	}
	var rp RecoveryPointResponse
	if err := json.NewDecoder(resp.Body).Decode(&rp); err != nil {
		return RecoveryPointResponse{}, err
	}

	return rp, nil
}

func (c *Client) GetInfoFileDownload(recoveryPointID string, itemID string, restoreSessionKey string, createdAt string) (FileDownloadResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.getInfoFileDownload(recoveryPointID, itemID))
	if err != nil {
		return FileDownloadResponse{}, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return FileDownloadResponse{}, err
	}
	req.Header.Add("X-Session-Created-At", createdAt)
	req.Header.Add("X-Restore-Session-Key", restoreSessionKey)

	resp, err := c.Do(req)
	if err != nil {
		return FileDownloadResponse{}, err
	}
	var fileDownload FileDownloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&fileDownload); err != nil {
		return FileDownloadResponse{}, err
	}

	return fileDownload, nil
}

func WalkerDir(dir string) (FileInfoRequest, error) {
	var fileInfoRequest FileInfoRequest

	err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			singleFile := FileInfo{
				ItemName:     path,
				Size:         fi.Size(),
				LastModified: fi.ModTime().Format("2006-01-02 15:04:05.000000"),
				ItemType:     "FILE",
				Mode:         fi.Mode().Perm().String(),
			}
			fileInfoRequest.Files = append(fileInfoRequest.Files, singleFile)
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	return fileInfoRequest, err
}

func EnsureDir(dirName string) error {
	err := os.MkdirAll(dirName, os.ModePerm)
	if err == nil || os.IsExist(err) {
		return nil
	} else {
		return err
	}
}

func CreateFile(name string) (*os.File, error) {
	file, err := os.Create(name)
	if err != nil {
		log.Println(err)
	}

	return file, nil
}

package backupapi

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/bizflycloud/bizfly-backup/pkg/volume"
	"github.com/restic/chunker"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

const ChunkUploadLowerBound = 8 * 1000 * 1000

// FileInfo ...
type FileInfo struct {
	ItemType    string    `json:"item_type"`
	ParentID    string    `json:"parent_id"`
	RpReference bool      `json:"rp_reference"`
	Attribute   Attribute `json:"attribute"`
}

// Attribute ...
type Attribute struct {
	ItemID       string `json:"item_id"`
	ItemName     string `json:"item_name"`
	Size         string `json:"size"`
	ChangedTime  string `json:"changed_time"`
	ModifiedTime string `json:"modified_time"`
	AccessTime   string `json:"access_time"`
	Mode         string `json:"mode"`
	GID          string `json:"gid"`
	UID          string `json:"uid"`
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
	Size         string `json:"size"`
	Status       string `json:"status"`
	UpdatedAt    string `json:"updated_at"`
	DeletedAt    string `json:"deleted_at"`
	Deleted      bool   `json:"deleted"`
	IsDir        bool   `json:"is_dir"`
}

// FilesResponse ...
type FilesResponse []File

// RecoveryPointResponse ...
type RecoveryPointResponse struct {
	Files []File `json:"files"`
	Total string `json:"total"`
}

// ChunkRequest ...
type ChunkRequest struct {
	Length string `json:"length"`
	Offset string `json:"offset"`
	Etag   string `json:"etag"`
}

// ChunkResponse ...
type ChunkResponse struct {
	ID           string       `json:"id"`
	Offset       string       `json:"offset"`
	Length       string       `json:"length"`
	Etag         string       `json:"etag"`
	Uri          string       `json:"uri"`
	DeletedAt    string       `json:"deleted_at"`
	Deleted      bool         `json:"deleted"`
	PresignedURL PresignedURL `json:"presigned_url"`
}

// PresignedURL ...
type PresignedURL struct {
	Head string `json:"head"`
	Put  string `json:"put"`
}

// InfoDownload ...
type InfoDownload struct {
	Get    string `json:"get"`
	Offset string `json:"offset"`
}

// FileDownloadResponse ...
type FileDownloadResponse struct {
	Info []InfoDownload `json:"info"`
}

// InfoPresignUrl ...
type InfoPresignUrl struct {
	ActionID string `json:"action_id"`
	Etag     string `json:"etag"`
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

func (c *Client) SaveFileInfo(recoveryPointID string, fi *FileInfo) (*File, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveFileInfoPath(recoveryPointID))
	if err != nil {
		return nil, err
	}

	req, err := c.NewRequest(http.MethodPost, reqURL, fi)
	if err != nil {
		return nil, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	var file File
	if err = json.NewDecoder(resp.Body).Decode(&file); err != nil {
		return nil, err
	}

	return &file, nil
}

func (c *Client) saveChunk(recoveryPointID string, fileID string, chunk *ChunkRequest) (*ChunkResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveChunkPath(recoveryPointID, fileID))
	if err != nil {
		return nil, err
	}

	req, err := c.NewRequest(http.MethodPost, reqURL, chunk)
	if err != nil {
		return nil, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var chunkResp ChunkResponse
	if err := json.NewDecoder(resp.Body).Decode(&chunkResp); err != nil {
		return nil, err
	}

	return &chunkResp, nil
}

func (c *Client) UploadFile(recoveryPointID string, actionID string, backupDir string, fileInfo FileInfo, volume volume.StorageVolume) error {
	file, err := os.Open(fileInfo.Attribute.ItemName)
	if err != nil {
		return err
	}

	_, err = c.SaveFileInfo(recoveryPointID, &fileInfo)
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
			Length: strconv.FormatUint(uint64(chunk.Length), 10),
			Offset: strconv.FormatUint(uint64(chunk.Start), 10),
			Etag:   key,
		}

		_, err = c.saveChunk(recoveryPointID, fileInfo.Attribute.ItemID, &chunkReq)
		if err != nil {
			return err
		}

		infoUrl := InfoPresignUrl{
			ActionID: actionID,
			Etag:     key,
		}
		chunkResp, err := c.infoPresignedUrl(recoveryPointID, fileInfo.Attribute.ItemID, &infoUrl)
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

	var file *os.File
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
		fileRestore := filepath.Join(absolutePathRealName, filepath.Base(f.RealName))

		if err := EnsureDir(absolutePathRealName); err != nil {
			return err
		}

		file, err = CreateFile(fileRestore)
		if err != nil {
			return err
		}

		for _, info := range infos.Info {
			errAcquire := sem.Acquire(ctx, 1)
			if errAcquire != nil {
				continue
			}
			offset, err := strconv.ParseInt(info.Offset, 10, 64)
			if err != nil {
				return err
			}
			key := info.Get

			group.Go(func() error {
				defer sem.Release(1)
				data, err := volume.GetObject(key)
				if err != nil {
					return err
				}
				_, errWriteFile := file.WriteAt(data, offset)
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
	defer file.Close()

	return nil
}

func (c *Client) GetListFilePath(recoveryPointID string) (*RecoveryPointResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.getListFilePath(recoveryPointID))
	if err != nil {
		return nil, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	var rp RecoveryPointResponse
	if err := json.NewDecoder(resp.Body).Decode(&rp); err != nil {
		return nil, err
	}

	return &rp, nil
}

func (c *Client) GetInfoFileDownload(recoveryPointID string, itemID string, restoreSessionKey string, createdAt string) (*FileDownloadResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.infoFile(recoveryPointID, itemID))
	if err != nil {
		return nil, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("X-Session-Created-At", createdAt)
	req.Header.Add("X-Restore-Session-Key", restoreSessionKey)

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	var fileDownload FileDownloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&fileDownload); err != nil {
		return nil, err
	}

	return &fileDownload, nil
}

func (c *Client) infoPresignedUrl(recoveryPointID string, itemID string, infoUrl *InfoPresignUrl) (*ChunkResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.infoFile(recoveryPointID, itemID))
	if err != nil {
		return nil, err
	}

	req, err := c.NewRequest(http.MethodPost, reqURL, infoUrl)
	if err != nil {
		return nil, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	var chunkResp ChunkResponse
	if err := json.NewDecoder(resp.Body).Decode(&chunkResp); err != nil {
		return nil, err
	}

	return &chunkResp, nil
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
		return nil, err
	}
	return file, nil
}

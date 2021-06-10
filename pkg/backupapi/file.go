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
	"strconv"
	"strings"
	"time"

	"github.com/bizflycloud/bizfly-backup/pkg/volume"
	"github.com/restic/chunker"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

const ChunkUploadLowerBound = 8 * 1000 * 1000

// ItemInfo ...
type ItemInfo struct {
	ItemType       string     `json:"item_type"`
	ParentItemID   string     `json:"parent_item_id,omitempty"`
	ChunkReference bool       `json:"chunk_reference"`
	Attributes     *Attribute `json:"attributes,omitempty"`
}

// Attribute ...
type Attribute struct {
	ID         string      `json:"id,omitempty"`
	ItemName   string      `json:"item_name,omitempty"`
	Size       int64       `json:"size,omitempty"`
	ItemType   string      `json:"item_type,omitempty"`
	IsDir      bool        `json:"is_dir,omitempty"`
	ChangeTime time.Time   `json:"change_time,omitempty"`
	ModifyTime time.Time   `json:"modify_time,omitempty"`
	AccessTime time.Time   `json:"access_time,omitempty"`
	Mode       string      `json:"mode,omitempty"`
	AccessMode os.FileMode `json:"access_mode,omitempty"`
	GID        uint32      `json:"gid,omitempty"`
	UID        uint32      `json:"uid,omitempty"`
}

// FileInfoRequest ...
type FileInfoRequest struct {
	Files []ItemInfo `json:"files"`
}

// File ...
type File struct {
	ContentType string      `json:"content_type"`
	CreatedAt   string      `json:"created_at"`
	Etag        string      `json:"etag"`
	ID          string      `json:"id"`
	ItemName    string      `json:"item_name"`
	ItemType    string      `json:"item_type"`
	Mode        string      `json:"mode"`
	AccessMode  os.FileMode `json:"access_mode"`
	RealName    string      `json:"real_name"`
	Size        int         `json:"size"`
	Status      string      `json:"status"`
	UpdatedAt   string      `json:"updated_at"`
	ChangeTime  time.Time   `json:"change_time"`
	ModifyTime  time.Time   `json:"modify_time"`
	AccessTime  time.Time   `json:"access_time"`
	Gid         uint32      `json:"gid"`
	UID         uint32      `json:"uid"`
}

// FilesResponse ...
type FilesResponse []File

// ItemsResponse ...
type ItemsResponse struct {
	Files []File `json:"files"`
	Total string `json:"total"`
}

// ChunkRequest ...
type ChunkRequest struct {
	Length int    `json:"length"`
	Offset int    `json:"offset"`
	Etag   string `json:"etag"`
}

// ChunkResponse ...
type ChunkResponse struct {
	ID           string       `json:"id"`
	Offset       int          `json:"offset"`
	Length       int          `json:"length"`
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

// ItemInfoLatest ...
type ItemInfoLatest struct {
	ID          string      `json:"id"`
	ItemType    string      `json:"item_type"`
	Mode        string      `json:"mode"`
	AccessMode  os.FileMode `json:"access_mode"`
	RealName    string      `json:"real_name"`
	Size        int         `json:"size"`
	ContentType string      `json:"content_type"`
	IsDir       bool        `json:"is_dir"`
	Status      string      `json:"status"`
	ItemName    string      `json:"item_name"`
	CreatedAt   string      `json:"created_at"`
	UpdatedAt   string      `json:"updated_at"`
	AccessTime  time.Time   `json:"access_time"`
	ChangeTime  time.Time   `json:"change_time"`
	ModifyTime  time.Time   `json:"modify_time"`
	Gid         int         `json:"gid"`
	UID         int         `json:"uid"`
}

func (c *Client) saveFileInfoPath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/file", recoveryPointID)
}

func (c *Client) getItemLatestPath(latestRecoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/path", latestRecoveryPointID)
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

func (c *Client) SaveFileInfo(recoveryPointID string, itemInfo *ItemInfo) (*File, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveFileInfoPath(recoveryPointID))
	if err != nil {
		return nil, err
	}

	req, err := c.NewRequest(http.MethodPost, reqURL, itemInfo)
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

func (c *Client) saveChunk(recoveryPointID string, itemID string, chunk *ChunkRequest) (*ChunkResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveChunkPath(recoveryPointID, itemID))
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

func (c *Client) GetItemLatest(latestRecoveryPointID string, filePath string) (*ItemInfoLatest, error) {
	if len(latestRecoveryPointID) == 0 {
		return &ItemInfoLatest{
			ID:         "",
			ChangeTime: time.Time{},
			ModifyTime: time.Time{},
		}, nil
	}

	reqURL, err := c.urlStringFromRelPath(c.getItemLatestPath(latestRecoveryPointID))
	if err != nil {
		return nil, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Add("path", filePath)
	req.URL.RawQuery = q.Encode()

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}

	var itemInfoLatest ItemInfoLatest
	if err := json.NewDecoder(resp.Body).Decode(&itemInfoLatest); err != nil {
		return nil, err
	}
	return &itemInfoLatest, nil
}

func (c *Client) ChunkFileToBackup(itemInfo ItemInfo, recoveryPointID string, actionID string, volume volume.StorageVolume) error {
	file, err := os.Open(itemInfo.Attributes.ItemName)
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
			Length: int(chunk.Length),
			Offset: int(chunk.Start),
			Etag:   key,
		}

		_, err = c.saveChunk(recoveryPointID, itemInfo.Attributes.ID, &chunkReq)
		if err != nil {
			return err
		}

		infoUrl := InfoPresignUrl{
			ActionID: actionID,
			Etag:     key,
		}
		chunkResp, err := c.infoPresignedUrl(recoveryPointID, itemInfo.Attributes.ID, &infoUrl)
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

func (c *Client) UploadFile(recoveryPointID string, actionID string, latestRecoveryPointID string, backupDir string, itemInfo ItemInfo, volume volume.StorageVolume) error {
	itemInfoLatest, err := c.GetItemLatest(latestRecoveryPointID, itemInfo.Attributes.ItemName)
	if err != nil {
		return err
	}
	log.Printf("Backup item: %+v\n", itemInfo)

	if timeToString(itemInfoLatest.ModifyTime) != timeToString(itemInfo.Attributes.ModifyTime) && timeToString(itemInfoLatest.ChangeTime) != timeToString(itemInfo.Attributes.ChangeTime) {
		log.Println("backup item with item change mtime, ctime")
		log.Println("Save file info", &itemInfo.Attributes.ItemName)
		itemInfo.ChunkReference = false
		_, err = c.SaveFileInfo(recoveryPointID, &itemInfo)
		if err != nil {
			return err
		}
		log.Println("Continue chunk file to backup")
		if itemInfo.ItemType == "FILE" {
			err := c.ChunkFileToBackup(itemInfo, recoveryPointID, actionID, volume)
			if err != nil {
				return nil
			}
		}
		return nil
	} else if timeToString(itemInfoLatest.ModifyTime) == timeToString(itemInfo.Attributes.ModifyTime) && timeToString(itemInfoLatest.ChangeTime) != timeToString(itemInfo.Attributes.ChangeTime) {
		// save info va reference chunk neu la file
		log.Println("backup item with item change ctime and mtime not change")
		_, err = c.SaveFileInfo(recoveryPointID, &itemInfo)
		if err != nil {
			return err
		}
		return nil
	} else {
		log.Println("backup item with item no change time")
		_, err = c.SaveFileInfo(recoveryPointID, &ItemInfo{
			ItemType:       itemInfo.ItemType,
			ParentItemID:   itemInfoLatest.ID,
			ChunkReference: itemInfo.ChunkReference,
		})

		if err != nil {
			return err
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

		if err := EnsureDirectory(absolutePathRealName); err != nil {
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

func (c *Client) GetListFilePath(recoveryPointID string) (*ItemsResponse, error) {
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
	var items ItemsResponse
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}

	return &items, nil
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

func EnsureDirectory(directoryName string) error {
	err := os.MkdirAll(directoryName, os.ModePerm)
	if err == nil || os.IsExist(err) {
		return nil
	} else {
		return err
	}
}

func CreateFile(path string) (*os.File, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func timeToString(time time.Time) string {
	return time.Format("2006-01-02 15:04:05.000000")
}

package backupapi

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/panjf2000/ants/v2"
	"go.uber.org/zap"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/bizflycloud/bizfly-backup/pkg/volume"
	"github.com/restic/chunker"
	log "github.com/sirupsen/logrus"
)

const ChunkUploadLowerBound = 8 * 1000 * 1000

// ItemInfo ...
type ItemInfo struct {
	ItemType       string     `json:"item_type"`
	ParentItemID   string     `json:"parent_item_id,omitempty"`
	ChunkReference bool       `json:"chunk_reference"`
	Attributes     *Attribute `json:"attributes,omitempty"`
}

type ChunkInfo struct {
	Start  uint
	Length uint
	Cut    uint64
	Data   []byte
}

// Attribute ...
type Attribute struct {
	ID          string      `json:"id"`
	ItemName    string      `json:"item_name"`
	SymlinkPath string      `json:"symlink_path,omitempty"`
	Size        int64       `json:"size"`
	ItemType    string      `json:"item_type"`
	IsDir       bool        `json:"is_dir"`
	ChangeTime  time.Time   `json:"change_time"`
	ModifyTime  time.Time   `json:"modify_time"`
	AccessTime  time.Time   `json:"access_time"`
	Mode        string      `json:"mode"`
	AccessMode  os.FileMode `json:"access_mode"`
	GID         uint32      `json:"gid"`
	UID         uint32      `json:"uid"`
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
	SymlinkPath string      `json:"symlink_path"`
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
type FileInfoResponse struct {
	Files []File `json:"files"`
	Total int    `json:"total"`
}

// Item ...
type Item struct {
	Mode        string      `json:"mode"`
	AccessMode  os.FileMode `json:"access_mode"`
	AccessTime  time.Time   `json:"access_time"`
	ChangeTime  time.Time   `json:"change_time"`
	ModifyTime  time.Time   `json:"modify_time"`
	ContentType string      `json:"content_type"`
	CreatedAt   string      `json:"created_at"`
	GID         uint32      `json:"gid"`
	UID         uint32      `json:"uid"`
	ID          string      `json:"id"`
	IsDir       bool        `json:"is_dir"`
	ItemName    string      `json:"item_name"`
	RealName    string      `json:"real_name"`
	SymlinkPath string      `json:"symlink_path"`
	ItemType    string      `json:"item_type"`
	Size        int         `json:"size"`
	Status      string      `json:"status"`
	UpdatedAt   string      `json:"updated_at"`
}

// ItemsResponse ...
type ItemsResponse struct {
	Items []Item `json:"items"`
	Total int    `json:"total"`
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

type ChunksResponse struct {
	Chunks []ChunkResponse `json:"chunks"`
	Total  uint64          `json:"total"`
}

// PresignedURL ...
type PresignedURL struct {
	Head string `json:"head"`
	Put  string `json:"put"`
}

// InfoDownload ...
type InfoDownload struct {
	Get    string `json:"get"`
	Offset int    `json:"offset"`
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

func (c *Client) getChunksInItem(recoveryPointID string, itemID string, page int) (int, *ChunksResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveChunkPath(recoveryPointID, itemID))
	if err != nil {
		return 0, nil, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, nil, err
	}

	itemsPerPage := 50
	q := req.URL.Query()
	q.Add("items_per_page", strconv.Itoa(itemsPerPage))
	q.Add("page", strconv.Itoa(page))
	req.URL.RawQuery = q.Encode()

	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var chunkResp ChunksResponse
	if err := json.NewDecoder(resp.Body).Decode(&chunkResp); err != nil {
		return 0, nil, err
	}

	totalItem := chunkResp.Total
	totalPage := int(math.Ceil(float64(totalItem) / float64(itemsPerPage)))

	return totalPage, &chunkResp, nil
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

func (c *Client) backupChunk(ctx context.Context, chunk ChunkInfo, itemInfo ItemInfo, recoveryPointID string, actionID string, volume volume.StorageVolume) (uint64, error) {
	select {
	case <-ctx.Done():
		c.logger.Debug("context backupChunk done")
		return 0, errors.New("backupChunk done")
	default:
		var stat uint64

		hash := md5.Sum(chunk.Data)
		key := hex.EncodeToString(hash[:])
		chunkReq := ChunkRequest{
			Length: int(chunk.Length),
			Offset: int(chunk.Start),
			Etag:   key,
		}

		_, err := c.saveChunk(recoveryPointID, itemInfo.Attributes.ID, &chunkReq)
		if err != nil {
			return stat, err
		}

		isExist, etag, err := c.HeadObject(volume, key)
		if err != nil {
			c.logger.Sugar().Errorf("backup chunk head object error: ", zap.Error(err))
			return 0, err
		}
		c.logger.Sugar().Info("Backup chunk ", key)
		if isExist {
			integrity := strings.Contains(etag, key)
			if !integrity {
				err := c.PutObject(volume, key, chunk.Data)
				if err != nil {
					return stat, err
				}
				stat += uint64(chunk.Length)
			} else {
				c.logger.Info("exists ", zap.String("etag", etag), zap.String("key", key))
			}
		} else {
			err = c.PutObject(volume, key, chunk.Data)
			if err != nil {
				return stat, err
			}
			stat += uint64(chunk.Length)
		}
		c.logger.Sugar().Info("Finished backup chunk ", key)
		return stat, nil
	}

}

// func (c *Client) ChunkFileToBackup(itemInfo ItemInfo, recoveryPointID string, actionID string, volume volume.StorageVolume, p *progress.Progress) error {
func (c *Client) ChunkFileToBackup(ctx context.Context, pool *ants.Pool, itemInfo ItemInfo, recoveryPointID string, actionID string, volume volume.StorageVolume) (uint64, error) {
	// p.Start()
	ctx, cancel := context.WithCancel(ctx)
	select {
	case <-ctx.Done():
		c.logger.Info("context done ChunkFileToBackup")
		cancel()
		return 0, nil
	default:
		var errBackupChunk error
		file, err := os.Open(itemInfo.Attributes.ItemName)
		if err != nil {
			return 0, err
		}
		chk := chunker.New(file, 0x3dea92648f6e83)
		buf := make([]byte, ChunkUploadLowerBound)
		var stat uint64

		var wg sync.WaitGroup
		for {
			chunk, err := chk.Next(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}

			// p.Report(s)

			temp := make([]byte, chunk.Length)
			length := copy(temp, chunk.Data)
			if uint(length) != chunk.Length {
				return 0, errors.New("copy chunk data error")
			}
			chunkToBackup := ChunkInfo{
				Start:  chunk.Start,
				Length: chunk.Length,
				Cut:    chunk.Cut,
				Data:   temp,
			}
			wg.Add(1)
			pool.Submit(c.backupChunkJob(ctx, &wg, &errBackupChunk, &stat, chunkToBackup, itemInfo, recoveryPointID, actionID, volume))
		}
		wg.Wait()

		if errBackupChunk != nil {
			return 0, errBackupChunk
		}

		return stat, nil
	}

}

type chunkJob func()

func (c *Client) backupChunkJob(ctx context.Context, wg *sync.WaitGroup, chErr *error, size *uint64,
	chunk ChunkInfo, itemInfo ItemInfo, recoveryPointID string, actionID string, volume volume.StorageVolume) chunkJob {
	return func() {
		select {
		case <-ctx.Done():
			return
		default:
			ctx, cancel := context.WithCancel(ctx)
			defer func() {
				c.logger.Sugar().Info("Done task ", chunk.Start)
				wg.Done()
			}()

			saveSize, err := c.backupChunk(ctx, chunk, itemInfo, recoveryPointID, actionID, volume)
			if err != nil {
				*chErr = err
				cancel()
				return
			}
			*size += saveSize
		}
	}
}

// func (c *Client) UploadFile(recoveryPointID string, actionID string, latestRecoveryPointID string, backupDir string, itemInfo ItemInfo, volume volume.StorageVolume, p *progress.Progress) error {
func (c *Client) UploadFile(ctx context.Context, pool *ants.Pool, recoveryPointID string, actionID string, latestRecoveryPointID string, itemInfo ItemInfo, volume volume.StorageVolume) (uint64, error) {

	select {
	case <-ctx.Done():
		c.logger.Debug("Context backup done")
		return 0, errors.New("context backup done")
	default:
		itemInfoLatest, err := c.GetItemLatest(latestRecoveryPointID, itemInfo.Attributes.ItemName)
		if err != nil {
			return 0, err
		}

		//c.logger.Sugar().Info("Backup item  ", itemInfo)
		// s := progress.Stat{}
		// backup item with item change ctime
		if !strings.EqualFold(timeToString(itemInfoLatest.ChangeTime), timeToString(itemInfo.Attributes.ChangeTime)) {
			// backup item with item change mtime
			if !strings.EqualFold(timeToString(itemInfoLatest.ModifyTime), timeToString(itemInfo.Attributes.ModifyTime)) {
				c.logger.Info("backup item with item change mtime, ctime")
				c.logger.Sugar().Info("Save file info ", itemInfo.Attributes.ItemName)
				itemInfo.ChunkReference = false
				_, err = c.SaveFileInfo(recoveryPointID, &itemInfo)
				if err != nil {
					c.logger.Error("c.SaveFileInfo ", zap.Error(err))
					return 0, err
				}
				if itemInfo.ItemType == "FILE" {
					c.logger.Sugar().Info("Continue chunk file to backup ", itemInfo.Attributes.ItemName)
					// err := c.ChunkFileToBackup(itemInfo, recoveryPointID, actionID, volume, p)
					storageSize, err := c.ChunkFileToBackup(ctx, pool, itemInfo, recoveryPointID, actionID, volume)
					if err != nil {
						c.logger.Error("c.ChunkFileToBackup ", zap.Error(err))
						return 0, err
					}
					return storageSize, nil
				}
				return 0, nil
			} else {
				// save info va reference chunk neu la file
				c.logger.Info("backup item with item change ctime and mtime not change")
				c.logger.Sugar().Info("Save file info ", itemInfo.Attributes.ItemName)
				itemInfo.ParentItemID = itemInfoLatest.ID
				_, err = c.SaveFileInfo(recoveryPointID, &itemInfo)
				if err != nil {
					c.logger.Error("err ", zap.Error(err))
					return 0, err
				}
				return 0, nil
			}

		} else {
			c.logger.Info("backup item with item no change time")
			c.logger.Sugar().Info("Save file info ", itemInfo.Attributes.ItemName)
			_, err = c.SaveFileInfo(recoveryPointID, &ItemInfo{
				ItemType:       itemInfo.ItemType,
				ParentItemID:   itemInfoLatest.ID,
				ChunkReference: itemInfo.ChunkReference,
			})

			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return 0, err
			}
		}

		return 0, nil
	}
}

func (c *Client) RestoreDirectory(recoveryPointID string, destDir string, volume volume.StorageVolume, restoreKey *AuthRestore) error {
	numGoroutine := int(float64(runtime.NumCPU()) * 0.2)
	if numGoroutine <= 1 {
		numGoroutine = 2
	}
	sem := semaphore.NewWeighted(int64(numGoroutine))
	ctx, cancel := context.WithCancel(context.Background())
	group, ctx := errgroup.WithContext(ctx)
	totalPage, _, err := c.GetListItemPath(recoveryPointID, 1)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}

	for page := 1; page <= totalPage; page++ {
		p := page
		_, rp, err := c.GetListItemPath(recoveryPointID, p)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return err
		}
		for _, item := range rp.Items {
			item := item
			err := sem.Acquire(ctx, 1)
			if err != nil {
				continue
			}
			group.Go(func() error {
				defer sem.Release(1)
				err := c.RestoreItem(ctx, recoveryPointID, destDir, item, volume, restoreKey)
				if err != nil {
					c.logger.Sugar().Info("Restore file error ", item.ItemName)
					cancel()
					return err
				}
				return nil
			})
		}
	}
	if err := group.Wait(); err != nil {
		c.logger.Error("Has a goroutine error ", zap.Error(err))
		return err
	}
	return nil
}

func (c *Client) RestoreItem(ctx context.Context, recoveryPointID string, destDir string, item Item, volume volume.StorageVolume, restoreKey *AuthRestore) error {
	select {
	case <-ctx.Done():
		return errors.New("context restore item done")
	default:
		pathItem := filepath.Join(destDir, item.RealName)
		switch item.ItemType {
		case "SYMLINK":
			err := c.restoreSymlink(pathItem, item)
			if err != nil {
				c.logger.Error("Error restore symlink ", zap.Error(err))
				return err
			}
		case "DIRECTORY":
			err := c.restoreDirectory(pathItem, item)
			if err != nil {
				c.logger.Error("Error restore directory ", zap.Error(err))
				return err
			}
		case "FILE":
			err := c.restoreFile(recoveryPointID, pathItem, item, volume, restoreKey)
			if err != nil {
				c.logger.Error("Error restore file ", zap.Error(err))
				return err
			}
		}
		return nil
	}
}

func (c *Client) restoreSymlink(target string, item Item) error {
	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			err := createSymlink(item.SymlinkPath, target, item.AccessMode, int(item.UID), int(item.GID))
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}
			return nil
		}
	} else {
		return err
	}
	_, ctimeLocal, _ := itemLocal(fi)
	if !strings.EqualFold(timeToString(ctimeLocal), timeToString(item.ChangeTime)) {
		c.logger.Sugar().Info("symlink change ctime. update mode, uid, gid ", item.RealName)
		err = os.Chmod(target, item.AccessMode)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return err
		}
		err = os.Chown(target, int(item.UID), int(item.GID))
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return err
		}
	}
	return nil
}

func (c *Client) restoreDirectory(target string, item Item) error {
	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			err := createDir(target, item.AccessMode, int(item.UID), int(item.GID), item.AccessTime, item.ModifyTime)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}
			return nil
		} else {
			return err
		}
	}
	_, ctimeLocal, _ := itemLocal(fi)
	if !strings.EqualFold(timeToString(ctimeLocal), timeToString(item.ChangeTime)) {
		c.logger.Sugar().Info("dir change ctime. update mode, uid, gid ", item.RealName)
		err = os.Chmod(target, item.AccessMode)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return err
		}
		err = os.Chown(target, int(item.UID), int(item.GID))
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return err
		}
	}
	return nil
}

func (c *Client) restoreFile(recoveryPointID string, target string, item Item, volume volume.StorageVolume, restoreKey *AuthRestore) error {
	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			c.logger.Sugar().Info("file not exist. create ", target)
			file, err := createFile(target, item.AccessMode, int(item.UID), int(item.GID))
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}

			err = c.downloadFile(file, recoveryPointID, item, volume, restoreKey)
			if err != nil {
				c.logger.Error("downloadFile error ", zap.Error(err))
				return err
			}
			return nil
		} else {
			return err
		}
	}
	c.logger.Sugar().Info("file exist ", target)
	_, ctimeLocal, mtimeLocal := itemLocal(fi)
	if !strings.EqualFold(timeToString(ctimeLocal), timeToString(item.ChangeTime)) {
		if !strings.EqualFold(timeToString(mtimeLocal), timeToString(item.ModifyTime)) {
			c.logger.Sugar().Info("file change mtime, ctime ", target)
			if err = os.Remove(target); err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}

			file, err := createFile(target, item.AccessMode, int(item.UID), int(item.GID))
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}

			err = c.downloadFile(file, recoveryPointID, item, volume, restoreKey)
			if err != nil {
				c.logger.Error("downloadFile error ", zap.Error(err))
				return err
			}
			return nil
		} else {
			c.logger.Sugar().Info("file change ctime. update mode, uid, gid ", target)
			err = os.Chmod(target, item.AccessMode)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}
			err = os.Chown(target, int(item.UID), int(item.GID))
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}
			err = os.Chtimes(target, item.AccessTime, item.ModifyTime)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}
		}
	} else {
		c.logger.Sugar().Info("file not change. not restore", target)
	}

	return nil
}

func (c *Client) downloadFile(file *os.File, recoveryPointID string, item Item, volume volume.StorageVolume, restoreKey *AuthRestore) error {
	totalPage, infos, err := c.getChunksInItem(recoveryPointID, item.ID, 1)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}
	if len(infos.Chunks) == 0 {
		return nil
	}

	for page := 1; page <= totalPage; page++ {
		_, infos, err := c.getChunksInItem(recoveryPointID, item.ID, page)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return err
		}
		for _, info := range infos.Chunks {
			offset, err := strconv.ParseInt(strconv.Itoa(info.Offset), 10, 64)
			if err != nil {
				return err
			}
			key := info.Etag

			data, err := c.GetObject(volume, key, restoreKey)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}
			_, errWriteFile := file.WriteAt(data, offset)
			if errWriteFile != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}
		}
	}

	err = os.Chmod(file.Name(), item.AccessMode)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}
	err = os.Chown(file.Name(), int(item.UID), int(item.GID))
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}
	err = os.Chtimes(file.Name(), item.AccessTime, item.ModifyTime)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}
	return nil
}

func (c *Client) GetListItemPath(recoveryPointID string, page int) (int, *ItemsResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.getListItemPath(recoveryPointID))
	if err != nil {
		return 0, nil, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, nil, err
	}

	itemsPerPage := 50
	q := req.URL.Query()
	q.Add("items_per_page", strconv.Itoa(itemsPerPage))
	q.Add("page", strconv.Itoa(page))
	req.URL.RawQuery = q.Encode()

	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	var items ItemsResponse
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return 0, nil, err
	}

	totalItem := items.Total
	totalPage := int(math.Ceil(float64(totalItem) / float64(itemsPerPage)))

	return totalPage, &items, nil
}

func (c *Client) GetInfoFileDownload(recoveryPointID string, itemFileID string, restoreSessionKey string, createdAt string) (*FileDownloadResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.infoFile(recoveryPointID, itemFileID))
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

func createSymlink(symlinkPath string, path string, mode fs.FileMode, uid int, gid int) error {
	dirName := filepath.Dir(path)
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		if err := os.MkdirAll(dirName, os.ModePerm); err != nil {
			return err
		}
	}

	err := os.Symlink(symlinkPath, path)
	if err != nil {
		log.Println(err)
	}

	err = os.Chmod(path, mode)
	if err != nil {
		log.Println(err)
	}

	err = os.Chown(path, uid, gid)
	if err != nil {
		log.Println(err)
	}

	return nil
}

func createDir(path string, mode fs.FileMode, uid int, gid int, atime time.Time, mtime time.Time) error {
	err := os.MkdirAll(path, os.ModePerm)
	if err != nil {
		return err
	}

	err = os.Chmod(path, mode)
	if err != nil {
		return err
	}

	err = os.Chown(path, uid, gid)
	if err != nil {
		return err
	}

	err = os.Chtimes(path, atime, mtime)
	if err != nil {
		return err
	}

	return nil
}

func createFile(path string, mode fs.FileMode, uid int, gid int) (*os.File, error) {
	dirName := filepath.Dir(path)
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		if err := os.MkdirAll(dirName, 0700); err != nil {
			return nil, err
		}

	}
	var file *os.File
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	err = os.Chmod(path, mode)
	if err != nil {
		return nil, err
	}

	err = os.Chown(path, uid, gid)
	if err != nil {
		return nil, err
	}

	return file, nil
}

func timeToString(time time.Time) string {
	return time.Format("2006-01-02 15:04:05.000000")
}

func itemLocal(fi fs.FileInfo) (time.Time, time.Time, time.Time) {
	var atimeLocal, ctimeLocal, mtimeLocal time.Time
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		atimeLocal = time.Unix(stat.Atim.Unix()).UTC()
		ctimeLocal = time.Unix(stat.Ctim.Unix()).UTC()
		mtimeLocal = time.Unix(stat.Mtim.Unix()).UTC()
	}
	return atimeLocal, ctimeLocal, mtimeLocal
}

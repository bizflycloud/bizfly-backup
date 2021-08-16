package backupapi

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bizflycloud/bizfly-backup/pkg/cache"
	"io"
	"io/fs"
	"io/ioutil"
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
	"time"

	"github.com/bizflycloud/bizfly-backup/pkg/progress"
	"github.com/bizflycloud/bizfly-backup/pkg/volume"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/panjf2000/ants/v2"
	"github.com/restic/chunker"
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
		c.logger.Error("err ", zap.Error(err))
		return 0, nil, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return 0, nil, err
	}

	itemsPerPage := 50
	q := req.URL.Query()
	q.Add("items_per_page", strconv.Itoa(itemsPerPage))
	q.Add("page", strconv.Itoa(page))
	req.URL.RawQuery = q.Encode()

	resp, err := c.Do(req)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return 0, nil, err
	}

	var b bytes.Buffer
	_, err = io.Copy(&b, resp.Body)
	if err != nil {
		c.logger.Error("Err write to buf ", zap.Error(err))
	} else {
		c.logger.Debug("Body Response", zap.String("Body", b.String()), zap.String("Request", req.URL.String()), zap.Int("StatusCode", resp.StatusCode))
	}

	defer resp.Body.Close()
	var chunkResp ChunksResponse
	if err := json.NewDecoder(&b).Decode(&chunkResp); err != nil {
		c.logger.Error("Err ", zap.Error(err), zap.String("Body Response", b.String()), zap.String("Request", req.URL.String()), zap.Int("StatusCode", resp.StatusCode))
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
		c.logger.Error("err ", zap.Error(err))
		return "", err
	}

	u := c.ServerURL.ResolveReference(relURL)
	return u.String(), nil
}

func (c *Client) SaveFileInfo(recoveryPointID string, itemInfo *ItemInfo) (*File, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveFileInfoPath(recoveryPointID))
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	req, err := c.NewRequest(http.MethodPost, reqURL, itemInfo)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	resp, err := c.Do(req)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	var b bytes.Buffer
	_, err = io.Copy(&b, resp.Body)
	if err != nil {
		c.logger.Error("Err write to buf ", zap.Error(err))
	} else {
		c.logger.Debug("Body", zap.String("Body Response", b.String()), zap.String("Request", req.URL.String()), zap.Int("StatusCode", resp.StatusCode))
	}

	defer resp.Body.Close()

	var file File

	if err = json.NewDecoder(&b).Decode(&file); err != nil {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			c.logger.Error("Err write to buf ", zap.Error(err))
		}
		sb := string(body)
		c.logger.Error("Err ", zap.Error(err), zap.String("Body Response", b.String()), zap.String("Body Request", sb), zap.String("Request", req.URL.String()), zap.Int("StatusCode", resp.StatusCode))
		return nil, err
	}

	return &file, nil
}

func (c *Client) saveChunk(recoveryPointID string, itemID string, chunk *ChunkRequest) (*ChunkResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveChunkPath(recoveryPointID, itemID))
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	req, err := c.NewRequest(http.MethodPost, reqURL, chunk)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	resp, err := c.Do(req)
	if err != nil {
		c.logger.Error("Err ", zap.Error(err))
		return nil, err
	}

	var b bytes.Buffer
	_, err = io.Copy(&b, resp.Body)
	if err != nil {
		c.logger.Error("Err write to buf ", zap.Error(err))
	} else {
		c.logger.Debug("Body", zap.String("Body Response", b.String()), zap.String("Request", req.URL.String()), zap.Int("StatusCode", resp.StatusCode))
	}

	defer resp.Body.Close()
	var chunkResp ChunkResponse
	if err := json.NewDecoder(&b).Decode(&chunkResp); err != nil {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			c.logger.Error("Err write to buf ", zap.Error(err))
		}
		sb := string(body)
		c.logger.Error("Err ", zap.Error(err), zap.String("Body Response", b.String()), zap.String("Body Request", sb), zap.String("Request", req.URL.String()), zap.Int("StatusCode", resp.StatusCode))
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
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	q := req.URL.Query()
	q.Add("path", filePath)
	req.URL.RawQuery = q.Encode()

	resp, err := c.Do(req)
	if err != nil {
		c.logger.Error("err ", zap.String("Request", req.URL.String()), zap.Error(err))
		return nil, err
	}

	var b bytes.Buffer
	_, err = io.Copy(&b, resp.Body)
	if err != nil {
		c.logger.Error("Err write to buf ", zap.Error(err))
	} else {
		c.logger.Debug("Body Response", zap.String("Body", b.String()), zap.String("Request", req.URL.String()), zap.Int("StatusCode", resp.StatusCode))
	}

	var itemInfoLatest ItemInfoLatest
	if err := json.NewDecoder(&b).Decode(&itemInfoLatest); err != nil {
		c.logger.Error("Err ", zap.Error(err), zap.String("Body Response", b.String()), zap.String("Request", req.URL.String()), zap.Int("StatusCode", resp.StatusCode))
		return nil, err
	}
	return &itemInfoLatest, nil
}

func (c *Client) backupChunk(ctx context.Context, chunk *cache.ChunkInfo, volume volume.StorageVolume) (uint64, error) {
	select {
	case <-ctx.Done():
		c.logger.Debug("context backupChunk done")
		return 0, errors.New("backupChunk done")
	default:
		var stat uint64

		hash := md5.Sum(chunk.Data)
		key := hex.EncodeToString(hash[:])
		chunk.Etag = key
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
					c.logger.Error("err ", zap.Error(err))
					return stat, err
				}
				stat += uint64(chunk.Length)
			} else {
				c.logger.Info("exists ", zap.String("etag", etag), zap.String("key", key))
			}
		} else {
			err = c.PutObject(volume, key, chunk.Data)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return stat, err
			}
			stat += uint64(chunk.Length)
		}
		c.logger.Sugar().Info("Finished backup chunk ", key)
		return stat, nil
	}

}

func (c *Client) ChunkFileToBackup(ctx context.Context, pool *ants.Pool, itemInfo *cache.Node,
	volume volume.StorageVolume, p *progress.Progress) (uint64, error) {
	ctx, cancel := context.WithCancel(ctx)
	select {
	case <-ctx.Done():
		c.logger.Info("context done ChunkFileToBackup")
		cancel()
		return 0, nil
	default:
		p.Start()
		s := progress.Stat{}
		var errBackupChunk error

		file, err := os.Open(itemInfo.Path)
		if err != nil {
			if os.IsNotExist(err) {
				//c.logger.Sugar().Info("item not exist ", itemInfo.Attributes.ItemName)
				s.ItemName = append(s.ItemName, itemInfo.Path)
				s.Errors = true
				p.Report(s)
				return 0, nil
			} else {
				c.logger.Error("err ", zap.Error(err))
				return 0, err
			}
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
				c.logger.Error("err ", zap.Error(err))
				return 0, err
			}

			temp := make([]byte, chunk.Length)
			length := copy(temp, chunk.Data)
			if uint(length) != chunk.Length {
				return 0, errors.New("copy chunk data error")
			}
			chunkToBackup := cache.ChunkInfo{
				Start:  chunk.Start,
				Length: chunk.Length,
				Data:   temp,
			}
			itemInfo.Content = append(itemInfo.Content, &chunkToBackup)
			wg.Add(1)
			_ = pool.Submit(c.backupChunkJob(ctx, &wg, &errBackupChunk, &stat, &chunkToBackup, volume, p))
		}
		wg.Wait()

		if errBackupChunk != nil {
			c.logger.Error("err backup chunk ", zap.Error(err))
			return 0, errBackupChunk
		}
		s.Items = 1
		p.Report(s)
		return stat, nil
	}
}

type chunkJob func()

func (c *Client) backupChunkJob(ctx context.Context, wg *sync.WaitGroup, chErr *error, size *uint64,
	chunk *cache.ChunkInfo, volume volume.StorageVolume, p *progress.Progress) chunkJob {
	return func() {
		p.Start()
		defer func() {
			c.logger.Sugar().Info("Done task ", chunk.Start)
			wg.Done()
		}()
		select {
		case <-ctx.Done():
			return
		default:
			s := progress.Stat{}
			ctx, cancel := context.WithCancel(ctx)
			saveSize, err := c.backupChunk(ctx, chunk, volume)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				*chErr = err
				s.Errors = true
				p.Report(s)
				cancel()
				return
			}
			s.Storage = saveSize
			s.Bytes = uint64(chunk.Length)
			p.Report(s)
			*size += saveSize
		}
	}
}

func (c *Client) UploadFile(ctx context.Context, pool *ants.Pool, lastInfo *cache.Node, itemInfo *cache.Node,
	volume volume.StorageVolume, p *progress.Progress) (uint64, error) {

	select {
	case <-ctx.Done():
		c.logger.Debug("Context backup done")
		return 0, errors.New("context backup done")
	default:

		s := progress.Stat{
			Items:  1,
			Errors: false,
		}

		// backup item with item change mtime
		if lastInfo == nil || !strings.EqualFold(timeToString(lastInfo.ModTime), timeToString(itemInfo.ModTime)) {
			c.logger.Info("backup item with item change mtime, ctime")

			storageSize, err := c.ChunkFileToBackup(ctx, pool, itemInfo, volume, p)
			if err != nil {
				c.logger.Error("c.ChunkFileToBackup ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return 0, err
			}
			p.Report(s)
			return storageSize, nil
		} else {
			itemInfo.Content = lastInfo.Content
		}
		p.Report(s)
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
				c.logger.Error("err ", zap.Error(err))
				continue
			}
			group.Go(func() error {
				defer sem.Release(1)
				err := c.RestoreItem(ctx, recoveryPointID, destDir, item, volume, restoreKey)
				if err != nil {
					c.logger.Error("Restore file error ", zap.Error(err), zap.String("item name", item.ItemName))
					return err
				}
				return nil
			})
		}
	}
	if err := group.Wait(); err != nil {
		c.logger.Error("Has a goroutine error ", zap.Error(err))
		cancel()
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
			c.logger.Sugar().Info("symlink not exist, create ", target)
			err := c.createSymlink(item.SymlinkPath, target, item.AccessMode, int(item.UID), int(item.GID))
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}
			return nil
		} else {
			c.logger.Error("err ", zap.Error(err))
			return err
		}
	}
	_, ctimeLocal, _, _, _ := ItemLocal(fi)
	if !strings.EqualFold(timeToString(ctimeLocal), timeToString(item.ChangeTime)) {
		c.logger.Sugar().Info("symlink change ctime. update mode, uid, gid ", item.RealName)
		err = os.Chmod(target, item.AccessMode)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return err
		}
		_ = SetChownItem(target, int(item.UID), int(item.GID))
	}
	return nil
}

func (c *Client) restoreDirectory(target string, item Item) error {
	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			c.logger.Sugar().Info("directory not exist, create ", target)
			err := c.createDir(target, item.AccessMode, int(item.UID), int(item.GID), item.AccessTime, item.ModifyTime)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}
			return nil
		} else {
			c.logger.Error("err ", zap.Error(err))
			return err
		}
	}
	_, ctimeLocal, _, _, _ := ItemLocal(fi)
	if !strings.EqualFold(timeToString(ctimeLocal), timeToString(item.ChangeTime)) {
		c.logger.Sugar().Info("dir change ctime. update mode, uid, gid ", item.RealName)
		err = os.Chmod(target, item.AccessMode)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return err
		}
		_ = SetChownItem(target, int(item.UID), int(item.GID))
	}
	return nil
}

func (c *Client) restoreFile(recoveryPointID string, target string, item Item, volume volume.StorageVolume, restoreKey *AuthRestore) error {
	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			c.logger.Sugar().Info("file not exist. create ", target)
			file, err := c.createFile(target, item.AccessMode, int(item.UID), int(item.GID))
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
			c.logger.Error("err ", zap.Error(err))
			return err
		}
	}
	c.logger.Sugar().Info("file exist ", target)
	_, ctimeLocal, mtimeLocal, _, _ := ItemLocal(fi)
	if !strings.EqualFold(timeToString(ctimeLocal), timeToString(item.ChangeTime)) {
		if !strings.EqualFold(timeToString(mtimeLocal), timeToString(item.ModifyTime)) {
			c.logger.Sugar().Info("file change mtime, ctime ", target)
			if err = os.Remove(target); err != nil {
				c.logger.Error("err ", zap.Error(err))
				return err
			}

			file, err := c.createFile(target, item.AccessMode, int(item.UID), int(item.GID))
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
			_ = SetChownItem(target, int(item.UID), int(item.GID))
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
				c.logger.Error("err ", zap.Error(err))
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
				c.logger.Error("err write file ", zap.Error(errWriteFile))
				return errWriteFile
			}
		}
	}

	err = os.Chmod(file.Name(), item.AccessMode)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}
	_ = SetChownItem(file.Name(), int(item.UID), int(item.GID))
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
		c.logger.Error("err ", zap.Error(err))
		return 0, nil, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return 0, nil, err
	}

	itemsPerPage := 50
	q := req.URL.Query()
	q.Add("items_per_page", strconv.Itoa(itemsPerPage))
	q.Add("page", strconv.Itoa(page))
	req.URL.RawQuery = q.Encode()

	resp, err := c.Do(req)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return 0, nil, err
	}

	var b bytes.Buffer
	_, err = io.Copy(&b, resp.Body)
	if err != nil {
		c.logger.Error("Err write to buf ", zap.Error(err))
	} else {
		c.logger.Debug("Body Response", zap.String("Body", b.String()), zap.String("Request", req.URL.String()), zap.Int("StatusCode", resp.StatusCode))
	}

	var items ItemsResponse
	if err := json.NewDecoder(&b).Decode(&items); err != nil {
		c.logger.Error("Err ", zap.Error(err))
		c.logger.Error("Body ", zap.String("Body", b.String()))
		return 0, nil, err
	}

	totalItem := items.Total
	totalPage := int(math.Ceil(float64(totalItem) / float64(itemsPerPage)))

	return totalPage, &items, nil
}

func (c *Client) createSymlink(symlinkPath string, path string, mode fs.FileMode, uid int, gid int) error {
	dirName := filepath.Dir(path)
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		if err := os.MkdirAll(dirName, os.ModePerm); err != nil {
			c.logger.Error("err ", zap.Error(err))
			return err
		}
	}

	err := os.Symlink(symlinkPath, path)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
	}

	err = os.Chmod(path, mode)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
	}
	_ = SetChownItem(path, uid, gid)
	return nil
}

func (c *Client) createDir(path string, mode fs.FileMode, uid int, gid int, atime time.Time, mtime time.Time) error {
	err := os.MkdirAll(path, os.ModePerm)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}

	err = os.Chmod(path, mode)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}

	_ = SetChownItem(path, uid, gid)
	err = os.Chtimes(path, atime, mtime)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}

	return nil
}

func (c *Client) createFile(path string, mode fs.FileMode, uid int, gid int) (*os.File, error) {
	dirName := filepath.Dir(path)
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		c.logger.Sugar().Info("file not exist ", dirName)
		if err := os.MkdirAll(dirName, 0700); err != nil {
			c.logger.Error("err ", zap.Error(err))
			return nil, err
		}
	}
	var file *os.File
	file, err := os.Create(path)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	err = os.Chmod(path, mode)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	_ = SetChownItem(path, uid, gid)
	return file, nil
}

func timeToString(time time.Time) string {
	return time.Format("2006-01-02 15:04:05.000000")
}

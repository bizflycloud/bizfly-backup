package backupapi

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
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
	"syscall"
	"time"

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

// func (c *Client) ChunkFileToBackup(itemInfo ItemInfo, recoveryPointID string, actionID string, volume volume.StorageVolume, p *progress.Progress) error {
func (c *Client) ChunkFileToBackup(itemInfo ItemInfo, recoveryPointID string, actionID string, volume volume.StorageVolume) (uint64, error) {
	// p.Start()

	file, err := os.Open(itemInfo.Attributes.ItemName)
	if err != nil {
		return 0, err
	}
	chk := chunker.New(file, 0x3dea92648f6e83)
	buf := make([]byte, ChunkUploadLowerBound)
	// s := progress.Stat{}
	var stat uint64
	for {
		chunk, err := chk.Next(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return stat, err
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
			return stat, err
		}
		// s.Bytes = uint64(chunk.Length)
		infoUrl := InfoPresignUrl{
			ActionID: actionID,
			Etag:     key,
		}
		chunkResp, err := c.infoPresignedUrl(recoveryPointID, itemInfo.Attributes.ID, &infoUrl)
		if err != nil {
			return stat, err
		}

		if chunkResp.PresignedURL.Head != "" {
			key = chunkResp.PresignedURL.Head
		}

		resp, err := volume.HeadObject(key)
		if err != nil {
			return stat, err
		}

		if etagHead, ok := resp.Header["Etag"]; ok {
			integrity := strings.Contains(etagHead[0], chunkResp.Etag)
			if !integrity {
				key = chunkResp.PresignedURL.Put
				_, err := volume.PutObject(key, chunk.Data)
				if err != nil {
					return stat, err
				}
				stat += uint64(chunk.Length)
				// s.Storage = uint64(chunk.Length)
			} else {
				log.Println("exists", etagHead[0], chunkResp.Etag)
			}
		} else {
			key = chunkResp.PresignedURL.Put
			_, err := volume.PutObject(key, chunk.Data)
			if err != nil {
				return stat, err
			}
			// s.Storage = uint64(chunk.Length)
			stat += uint64(chunk.Length)
		}
		// p.Report(s)
	}

	return stat, nil
}

// func (c *Client) UploadFile(recoveryPointID string, actionID string, latestRecoveryPointID string, backupDir string, itemInfo ItemInfo, volume volume.StorageVolume, p *progress.Progress) error {
func (c *Client) UploadFile(recoveryPointID string, actionID string, latestRecoveryPointID string, itemInfo ItemInfo, volume volume.StorageVolume) (uint64, error) {

	// p.Start()

	itemInfoLatest, err := c.GetItemLatest(latestRecoveryPointID, itemInfo.Attributes.ItemName)
	if err != nil {
		return 0, err
	}

	if itemInfo.ItemType == "SYMLINK" {
		log.Printf("Save file info %v", itemInfo.Attributes.ItemName)
		link, err := os.Readlink(itemInfo.Attributes.ItemName)
		if err != nil {
			log.Error(err)
		}

		// log.Println("link", link)
		itemInfo.ParentItemID = itemInfoLatest.ID
		itemInfo.Attributes.SymlinkPath = link
		_, err = c.SaveFileInfo(recoveryPointID, &itemInfo)
		if err != nil {
			log.Error(err)
			return 0, err
		}
	}

	fmt.Printf("\n")
	log.Printf("Backup item: %+v\n", itemInfo)
	// s := progress.Stat{}
	// backup item with item change ctime
	if !strings.EqualFold(timeToString(itemInfoLatest.ChangeTime), timeToString(itemInfo.Attributes.ChangeTime)) {
		// backup item with item change mtime
		if !strings.EqualFold(timeToString(itemInfoLatest.ModifyTime), timeToString(itemInfo.Attributes.ModifyTime)) {
			log.Println("backup item with item change mtime, ctime")
			log.Printf("Save file info %v", itemInfo.Attributes.ItemName)
			itemInfo.ChunkReference = false
			_, err = c.SaveFileInfo(recoveryPointID, &itemInfo)
			if err != nil {
				log.Error(err)
				return 0, err
			}
			// switch itemInfo.ItemType {
			// case "FILE":
			// 	s.Files = 1
			// case "DIRECTORY":
			// 	s.Dirs = 1
			// }
			// p.Report(s)
			if itemInfo.ItemType == "FILE" {
				log.Println("Continue chunk file to backup")
				// err := c.ChunkFileToBackup(itemInfo, recoveryPointID, actionID, volume, p)
				storageSize, err := c.ChunkFileToBackup(itemInfo, recoveryPointID, actionID, volume)
				if err != nil {
					log.Error(err)
					return 0, err
				}
				return storageSize, nil
			}
			return 0, nil
		} else {
			// save info va reference chunk neu la file
			log.Println("backup item with item change ctime and mtime not change")
			log.Printf("Save file info %v", itemInfo.Attributes.ItemName)
			itemInfo.ParentItemID = itemInfoLatest.ID
			_, err = c.SaveFileInfo(recoveryPointID, &itemInfo)
			if err != nil {
				log.Error(err)
				return 0, err
			}
			// switch itemInfo.ItemType {
			// case "FILE":
			// 	s.Bytes = uint64(itemInfo.Attributes.Size)
			// 	s.Files = 1
			// case "DIRECTORY":
			// 	s.Dirs = 1
			// }
			// p.Report(s)
			return 0, nil
		}

	} else {
		log.Println("backup item with item no change time")
		log.Printf("Save file info %v", itemInfo.Attributes.ItemName)
		_, err = c.SaveFileInfo(recoveryPointID, &ItemInfo{
			ItemType:       itemInfo.ItemType,
			ParentItemID:   itemInfoLatest.ID,
			ChunkReference: itemInfo.ChunkReference,
		})

		if err != nil {
			log.Error(err)
			return 0, err
		}
		// switch itemInfo.ItemType {
		// case "FILE":
		// 	s.Bytes = uint64(itemInfo.Attributes.Size)
		// 	s.Files = 1
		// case "DIRECTORY":
		// 	s.Dirs = 1
		// }
		// p.Report(s)
	}

	return 0, nil
}

func (c *Client) RestoreFile(recoveryPointID string, destDir string, volume volume.StorageVolume, restoreSessionKey string, createdAt string) error {
	sem := semaphore.NewWeighted(int64(5 * runtime.NumCPU()))
	group, ctx := errgroup.WithContext(context.Background())

	totalPage, _, err := c.GetListItemPath(recoveryPointID, 1)
	if err != nil {
		log.Error(err)
		return err
	}
	var file *os.File
	if *totalPage > 0 {
		for page := 1; page <= *totalPage; page++ {

			err := sem.Acquire(ctx, 1)
			if err != nil {
				continue
			}

			p := page
			group.Go(func() error {
				defer sem.Release(1)
				_, rp, err := c.GetListItemPath(recoveryPointID, p)
				if err != nil {
					log.Error(err)
					return err
				}

				for _, item := range rp.Items {
					path := filepath.Join(destDir, item.RealName)
					log.Println("restore item", path)
					if fi, err := os.Stat(path); os.IsNotExist(err) {
						switch item.ItemType {
						case "DIRECTORY":
							log.Println("dir not exist. create", path)
							err := createDir(path, item.AccessMode, int(item.UID), int(item.GID), item.AccessTime, item.ModifyTime)
							if err != nil {
								log.Error(err)
								return err
							}

						case "FILE":
							log.Println("file not exist. create", path)
							if file, err = createFile(path, item.AccessMode, int(item.UID), int(item.GID)); err != nil {
								log.Error(err)
								return err
							}
							infos, err := c.GetInfoFileDownload(recoveryPointID, item.ID, restoreSessionKey, createdAt)
							if err != nil {
								log.Error(err)
								return err
							}

							if len(infos.Info) == 0 {
								break
							}

							for _, info := range infos.Info {
								offset, err := strconv.ParseInt(strconv.Itoa(info.Offset), 10, 64)
								if err != nil {
									return err
								}
								key := info.Get

								data, err := volume.GetObject(key)
								if err != nil {
									log.Error(err)
									return err
								}
								_, errWriteFile := file.WriteAt(data, offset)
								if errWriteFile != nil {
									log.Error(err)
									return err
								}
							}
							err = os.Chtimes(file.Name(), item.AccessTime, item.ModifyTime)
							if err != nil {
								log.Error(err)
								return err
							}
						}
					} else {
						switch item.ItemType {
						case "DIRECTORY":
							log.Println("dir exist", path)
							_, ctimeLocal, _ := itemLocal(fi)
							if !strings.EqualFold(timeToString(ctimeLocal), timeToString(item.ChangeTime)) {
								log.Printf("dir %s change ctime. update mode, uid, gid", item.RealName)
								err = os.Chmod(path, item.AccessMode)
								if err != nil {
									log.Error(err)
									return err
								}
								err = os.Chown(path, int(item.UID), int(item.GID))
								if err != nil {
									log.Error(err)
									return err
								}
							}
						case "FILE":
							log.Println("file exist", path)
							_, ctimeLocal, mtimeLocal := itemLocal(fi)
							if !strings.EqualFold(timeToString(ctimeLocal), timeToString(item.ChangeTime)) {
								if !strings.EqualFold(timeToString(mtimeLocal), timeToString(item.ModifyTime)) {
									log.Printf("file %s change mtime, ctime", path)

									if err = os.Remove(path); err != nil {
										log.Error(err)
										return err
									}

									if file, err = createFile(path, item.AccessMode, int(item.UID), int(item.GID)); err != nil {
										log.Error(err)
										return err
									}

									infos, err := c.GetInfoFileDownload(recoveryPointID, item.ID, restoreSessionKey, createdAt)
									if err != nil {
										log.Error(err)
										return err
									}

									if len(infos.Info) == 0 {
										break
									}

									for _, info := range infos.Info {
										offset, err := strconv.ParseInt(strconv.Itoa(info.Offset), 10, 64)
										if err != nil {
											return err
										}
										key := info.Get

										data, err := volume.GetObject(key)
										if err != nil {
											log.Error(err)
											return err
										}
										_, errWriteFile := file.WriteAt(data, offset)
										if errWriteFile != nil {
											log.Error(err)
											return err
										}
									}
									err = os.Chtimes(file.Name(), item.AccessTime, item.ModifyTime)
									if err != nil {
										log.Error(err)
										return err
									}
								} else {
									log.Printf("file %s change ctime. update mode, uid, gid", path)
									err = os.Chmod(path, item.AccessMode)
									if err != nil {
										log.Error(err)
										return err
									}
									err = os.Chown(path, int(item.UID), int(item.GID))
									if err != nil {
										log.Error(err)
										return err
									}
								}
							} else {
								log.Printf("file %s not change. not restore", path)
							}
						}
					}
				}
				return nil
			})
		}
	}
	defer file.Close()

	if err := group.Wait(); err != nil {
		return err
	}
	return nil
}

func (c *Client) GetListItemPath(recoveryPointID string, page int) (*int, *ItemsResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.getListItemPath(recoveryPointID))
	if err != nil {
		return nil, nil, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, nil, err
	}

	itemsPerPage := 50
	q := req.URL.Query()
	q.Add("items_per_page", strconv.Itoa(itemsPerPage))
	q.Add("page", strconv.Itoa(page))
	req.URL.RawQuery = q.Encode()

	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	var items ItemsResponse
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, nil, err
	}

	totalItem := items.Total
	totalPage := int(math.Ceil(float64(totalItem) / float64(itemsPerPage)))

	return &totalPage, &items, nil
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

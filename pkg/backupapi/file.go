package backupapi

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bizflycloud/bizfly-backup/pkg/cache"
	"github.com/bizflycloud/bizfly-backup/pkg/progress"
	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"
	"github.com/bizflycloud/bizfly-backup/pkg/support"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/restic/chunker"
)

const ChunkUploadLowerBound = 8 * 1000 * 1000

func (c *Client) SaveChunks(cacheWriter *cache.Repository, chunks *cache.Chunk) error {
	err := cacheWriter.SaveChunk(chunks)
	if err != nil {
		c.logger.Error("Write list chunks error", zap.Error(err))
		return err
	}
	return nil
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

func (c *Client) backupChunk(ctx context.Context, data []byte, chunk *cache.ChunkInfo, cacheWriter *cache.Repository, chunks *cache.Chunk, storageVault storage_vault.StorageVault) (uint64, error) {
	select {
	case <-ctx.Done():
		c.logger.Debug("context backupChunk done")
		return 0, errors.New("backupChunk done")
	default:
		var stat uint64

		hash := md5.Sum(data)
		key := hex.EncodeToString(hash[:])
		chunk.Etag = key
		c.mu.Lock()
		if count, ok := chunks.Chunks[key]; ok {
			chunks.Chunks[key] = count + 1
		} else {
			chunks.Chunks[key] = 1
		}
		c.mu.Unlock()

		// Put object
		c.logger.Sugar().Info("Scan chunk ", key)
		err := c.PutObject(storageVault, key, data)
		if err != nil {
			c.logger.Error("err put object", zap.Error(err))
			return stat, err
		}
		stat += uint64(chunk.Length)

		// Save chunks
		c.logger.Sugar().Info("Save chunk to chunk.json ", key)
		c.mu.Lock()
		errSaveChunks := c.SaveChunks(cacheWriter, chunks)
		if errSaveChunks != nil {
			c.logger.Error("err save chunks ", zap.Error(errSaveChunks))
			return 0, errSaveChunks
		}
		c.mu.Unlock()
		return stat, nil
	}
}

func (c *Client) ChunkFileToBackup(ctx context.Context, itemInfo *cache.Node, cacheWriter *cache.Repository, chunks *cache.Chunk,
	storageVault storage_vault.StorageVault, p *progress.Progress, numGoroutine int) (uint64, error) {
	cxtUploadFile, cancel := context.WithCancel(context.Background())
	defer cancel()
	group, context := errgroup.WithContext(cxtUploadFile)
	sem := semaphore.NewWeighted(int64(numGoroutine))
	select {
	case <-ctx.Done():
		c.logger.Info("context done ChunkFileToBackup")
		cancel()
		return 0, nil
	default:
		p.Start()
		s := progress.Stat{}

		file, err := os.Open(itemInfo.AbsolutePath)
		if err != nil {
			if os.IsNotExist(err) {
				s.ItemName = append(s.ItemName, itemInfo.AbsolutePath)
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
		var mu sync.Mutex
		hash := sha256.New()
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
			}
			hash.Write(temp)
			itemInfo.Content = append(itemInfo.Content, &chunkToBackup)

			errAcquire := sem.Acquire(context, 1)
			if errAcquire != nil {
				c.logger.Sugar().Debug("Acquire err = %+v\n", errAcquire)
				continue
			}
			group.Go(func() error {
				defer sem.Release(1)
				saveSize, err := c.backupChunk(ctx, temp, &chunkToBackup, cacheWriter, chunks, storageVault)
				if err != nil {
					s.Errors = true
					p.Report(s)
					cancel()
					return err
				}
				s.Storage = saveSize
				s.Bytes = uint64(chunkToBackup.Length)
				p.Report(s)

				mu.Lock()
				stat += saveSize
				mu.Unlock()

				return nil
			})
		}
		if errGroupWait := group.Wait(); errGroupWait != nil {
			c.logger.Error("Has a goroutine error ", zap.Error(errGroupWait))
			cancel()
			return 0, errGroupWait
		}

		itemInfo.Sha256Hash = hash.Sum(nil)
		return stat, nil
	}
}

func (c *Client) UploadFile(ctx context.Context, lastInfo *cache.Node, itemInfo *cache.Node, cacheWriter *cache.Repository, chunks *cache.Chunk,
	storageVault storage_vault.StorageVault, p *progress.Progress, numGoroutine int) (uint64, error) {
	select {
	case <-ctx.Done():
		c.logger.Debug("Context backup done")
		return 0, errors.New("context backup done")
	default:

		s := progress.Stat{}

		// backup item with item change mtime
		if lastInfo == nil || !strings.EqualFold(timeToString(lastInfo.ModTime), timeToString(itemInfo.ModTime)) {
			c.logger.Info("backup item with item change mtime, ctime")

			storageSize, err := c.ChunkFileToBackup(ctx, itemInfo, cacheWriter, chunks, storageVault, p, numGoroutine)
			if err != nil {
				c.logger.Error("c.ChunkFileToBackup ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return 0, err
			}
			p.Report(s)
			return storageSize, nil
		} else {
			c.logger.Info("backup item with item no change mtime, ctime")
			for _, content := range lastInfo.Content {
				c.mu.Lock()

				if count, ok := chunks.Chunks[content.Etag]; ok {
					chunks.Chunks[content.Etag] = count + 1
				} else {
					chunks.Chunks[content.Etag] = 1
				}
				c.mu.Unlock()
			}
			itemInfo.Content = lastInfo.Content
			itemInfo.Sha256Hash = lastInfo.Sha256Hash
		}
		p.Report(s)
		return 0, nil
	}
}

func (c *Client) RestoreDirectory(index cache.Index, destDir string, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, p *progress.Progress, numGoroutine int) error {
	p.Start()
	s := progress.Stat{}
	sem := semaphore.NewWeighted(int64(numGoroutine))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	group, ctx := errgroup.WithContext(ctx)

	for _, item := range index.Items {
		item := item
		err := sem.Acquire(ctx, 1)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			continue
		}
		group.Go(func() error {
			defer sem.Release(1)
			err := c.RestoreItem(ctx, destDir, *item, storageVault, restoreKey, p)
			if err != nil {
				c.logger.Error("Restore file error ", zap.Error(err), zap.String("item name", item.AbsolutePath))
				s.Errors = true
				p.Report(s)
				return err
			}
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		c.logger.Error("Has a goroutine error ", zap.Error(err))
		cancel()
		return err
	}
	return nil
}

func (c *Client) RestoreItem(ctx context.Context, destDir string, item cache.Node, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, p *progress.Progress) error {
	select {
	case <-ctx.Done():
		return errors.New("context restore item done")
	default:
		p.Start()
		s := progress.Stat{}
		var pathItem string
		if destDir == item.BasePath {
			pathItem = item.AbsolutePath
		} else {
			pathItem = filepath.Join(destDir, item.RelativePath)
		}
		switch item.Type {
		case "symlink":
			err := c.restoreSymlink(pathItem, item, p)
			if err != nil {
				c.logger.Error("Error restore symlink ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
			p.Report(s)
		case "dir":
			err := c.restoreDirectory(pathItem, item, p)
			if err != nil {
				c.logger.Error("Error restore directory ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
			p.Report(s)
		case "file":
			err := c.restoreFile(pathItem, item, storageVault, restoreKey, p)
			if err != nil {
				c.logger.Error("Error restore file ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
			p.Report(s)
		}
		s.Items = 1
		p.Report(s)
		return nil
	}
}

func (c *Client) restoreSymlink(target string, item cache.Node, p *progress.Progress) error {
	p.Start()
	s := progress.Stat{}
	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			c.logger.Sugar().Info("symlink not exist, create ", target)
			err := c.createSymlink(item.LinkTarget, target, item.Mode, int(item.UID), int(item.GID))
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
			return nil
		} else {
			c.logger.Error("err ", zap.Error(err))
			s.Errors = true
			p.Report(s)
			return err
		}
	}
	_, ctimeLocal, _, _, _, _ := support.ItemLocal(fi)
	if !strings.EqualFold(timeToString(ctimeLocal), timeToString(item.ChangeTime)) {
		c.logger.Sugar().Info("symlink change ctime. update mode, uid, gid ", item.Name)
		err = os.Chmod(target, item.Mode)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			s.Errors = true
			p.Report(s)
			return err
		}
		_ = support.SetChownItem(target, int(item.UID), int(item.GID))
	}
	return nil
}

func (c *Client) restoreDirectory(target string, item cache.Node, p *progress.Progress) error {
	p.Start()
	s := progress.Stat{}
	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			c.logger.Sugar().Info("directory not exist, create ", target)
			err := c.createDir(target, os.ModeDir|item.Mode, int(item.UID), int(item.GID), item.AccessTime, item.ModTime)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
			return nil
		} else {
			c.logger.Error("err ", zap.Error(err))
			s.Errors = true
			p.Report(s)
			return err
		}
	}
	_, ctimeLocal, _, _, _, _ := support.ItemLocal(fi)
	if !strings.EqualFold(timeToString(ctimeLocal), timeToString(item.ChangeTime)) {
		c.logger.Sugar().Info("dir change ctime. update mode, uid, gid ", item.Name)
		err = os.Chmod(target, os.ModeDir|item.Mode)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			s.Errors = true
			p.Report(s)
			return err
		}
		_ = support.SetChownItem(target, int(item.UID), int(item.GID))
	}
	return nil
}

func (c *Client) restoreFile(target string, item cache.Node, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, p *progress.Progress) error {
	p.Start()
	s := progress.Stat{}
	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			c.logger.Sugar().Info("file not exist. create ", target)
			file, err := c.createFile(target, item.Mode, int(item.UID), int(item.GID))
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}

			err = c.downloadFile(file, item, storageVault, restoreKey, p)
			if err != nil {
				c.logger.Error("downloadFile error ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
			p.Report(s)
			return nil
		} else {
			c.logger.Error("err ", zap.Error(err))
			s.Errors = true
			p.Report(s)
			return err
		}
	}
	c.logger.Sugar().Info("file exist ", target)
	_, ctimeLocal, mtimeLocal, _, _, _ := support.ItemLocal(fi)
	if !strings.EqualFold(timeToString(ctimeLocal), timeToString(item.ChangeTime)) {
		if !strings.EqualFold(timeToString(mtimeLocal), timeToString(item.ModTime)) {
			c.logger.Sugar().Info("file change mtime, ctime ", target)
			if err = os.Remove(target); err != nil {
				c.logger.Error("err ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}

			file, err := c.createFile(target, item.Mode, int(item.UID), int(item.GID))
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}

			err = c.downloadFile(file, item, storageVault, restoreKey, p)
			if err != nil {
				c.logger.Error("downloadFile error ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
			return nil
		} else {
			c.logger.Sugar().Info("file change ctime. update mode, uid, gid ", target)
			err = os.Chmod(target, item.Mode)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
			_ = support.SetChownItem(target, int(item.UID), int(item.GID))
			err = os.Chtimes(target, item.AccessTime, item.ModTime)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
		}
	} else {
		c.logger.Sugar().Info("file not change. not restore", target)
	}

	return nil
}

func (c *Client) downloadFile(file *os.File, item cache.Node, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, p *progress.Progress) error {
	p.Start()
	s := progress.Stat{}
	for _, info := range item.Content {
		offset := info.Start
		key := info.Etag
		length := info.Length

		data, err := c.GetObject(storageVault, key, restoreKey)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			s.Errors = true
			p.Report(s)
			return err
		}
		s.Bytes = uint64(length)
		s.Storage = uint64(length)
		p.Report(s)
		_, errWriteFile := file.WriteAt(data, int64(offset))
		if errWriteFile != nil {
			c.logger.Error("err write file ", zap.Error(errWriteFile))
			s.Errors = true
			p.Report(s)
			return errWriteFile
		}
	}

	err := os.Chmod(file.Name(), item.Mode)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		s.Errors = true
		p.Report(s)
		return err
	}
	_ = support.SetChownItem(file.Name(), int(item.UID), int(item.GID))
	err = os.Chtimes(file.Name(), item.AccessTime, item.ModTime)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		s.Errors = true
		p.Report(s)
		return err
	}
	return nil
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
	_ = support.SetChownItem(path, uid, gid)
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

	_ = support.SetChownItem(path, uid, gid)
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

	_ = support.SetChownItem(path, uid, gid)
	return file, nil
}

func timeToString(time time.Time) string {
	return time.Format("2006-01-02 15:04:05.000000")
}

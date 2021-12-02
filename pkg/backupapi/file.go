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
	"runtime"
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

	"github.com/panjf2000/ants/v2"
	"github.com/restic/chunker"
)

const ChunkUploadLowerBound = 8 * 1000 * 1000

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

func (c *Client) backupChunk(ctx context.Context, data []byte, chunk *cache.ChunkInfo, cacheWriter *cache.Repository, storageVault storage_vault.StorageVault, pipe chan<- *cache.Chunk, rpID, bdID string) (uint64, error) {
	select {
	case <-ctx.Done():
		c.logger.Debug("context backupChunk done")
		return 0, errors.New("backupChunk done")
	default:
		var stat uint64

		hash := md5.Sum(data)
		key := hex.EncodeToString(hash[:])
		chunk.Etag = key

		chunks := cache.NewChunk(bdID, rpID)

		chunks.Chunks[key] = 1

		pipe <- chunks

		// Put object
		c.logger.Sugar().Info("Scan chunk ", key)
		err := c.PutObject(storageVault, key, data)
		if err != nil {
			c.logger.Error("err put object", zap.Error(err))
			return stat, err
		}
		stat += uint64(chunk.Length)
		return stat, nil
	}
}

func (c *Client) ChunkFileToBackup(ctx context.Context, pool *ants.Pool, itemInfo *cache.Node, cacheWriter *cache.Repository,
	storageVault storage_vault.StorageVault, p *progress.Progress, pipe chan<- *cache.Chunk, rpID, bdID string) (uint64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	select {
	case <-ctx.Done():
		c.logger.Info("context done ChunkFileToBackup")
		cancel()
		return 0, nil
	default:
		p.Start()
		s := progress.Stat{}
		var errBackupChunk error

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
		hash := sha256.New()
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
			}
			hash.Write(temp)
			itemInfo.Content = append(itemInfo.Content, &chunkToBackup)
			wg.Add(1)
			_ = pool.Submit(c.backupChunkJob(ctx, &wg, &errBackupChunk, &stat, temp, &chunkToBackup, cacheWriter, storageVault, p, pipe, bdID, rpID))
		}
		wg.Wait()

		if errBackupChunk != nil {
			c.logger.Error("err backup chunk ", zap.Error(errBackupChunk))
			return 0, errBackupChunk
		}
		itemInfo.Sha256Hash = hash.Sum(nil)
		return stat, nil
	}
}

type chunkJob func()

func (c *Client) backupChunkJob(ctx context.Context, wg *sync.WaitGroup, chErr *error, size *uint64,
	data []byte, chunk *cache.ChunkInfo, cacheWriter *cache.Repository, storageVault storage_vault.StorageVault, p *progress.Progress, pipe chan<- *cache.Chunk, rpID, bdID string) chunkJob {
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
			defer cancel()
			saveSize, err := c.backupChunk(ctx, data, chunk, cacheWriter, storageVault, pipe, bdID, rpID)
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

func (c *Client) UploadFile(ctx context.Context, pool *ants.Pool, lastInfo *cache.Node, itemInfo *cache.Node, cacheWriter *cache.Repository,
	storageVault storage_vault.StorageVault, p *progress.Progress, pipe chan<- *cache.Chunk, rpID, bdID string) (uint64, error) {

	select {
	case <-ctx.Done():
		c.logger.Debug("Context backup done")
		return 0, errors.New("context backup done")
	default:

		s := progress.Stat{}

		// backup item with item change mtime
		if lastInfo == nil || !strings.EqualFold(timeToString(lastInfo.ModTime), timeToString(itemInfo.ModTime)) {
			c.logger.Info("backup item with item change mtime, ctime")

			storageSize, err := c.ChunkFileToBackup(ctx, pool, itemInfo, cacheWriter, storageVault, p, pipe, bdID, rpID)
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
				chunks := cache.NewChunk(bdID, rpID)

				chunks.Chunks[content.Etag] = 1

				pipe <- chunks
			}

			itemInfo.Content = lastInfo.Content
			itemInfo.Sha256Hash = lastInfo.Sha256Hash
		}
		p.Report(s)
		return 0, nil
	}
}

func (c *Client) RestoreDirectory(index cache.Index, destDir string, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, p *progress.Progress) error {
	p.Start()
	s := progress.Stat{}
	numGoroutine := int(float64(runtime.NumCPU()) * 0.2)
	if numGoroutine <= 1 {
		numGoroutine = 2
	}
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

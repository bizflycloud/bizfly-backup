package backupapi

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bizflycloud/bizfly-backup/pkg/cache"
	"github.com/bizflycloud/bizfly-backup/pkg/progress"
	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"
	"github.com/bizflycloud/bizfly-backup/pkg/support"
	"github.com/bizflycloud/bizfly-backup/pkg/vss"
	"github.com/cenkalti/backoff"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/panjf2000/ants/v2"
	chunkers "github.com/PlakarKorp/go-cdc-chunkers"
	_ "github.com/PlakarKorp/go-cdc-chunkers/chunkers/fastcdc"
	"github.com/spf13/viper"
)

const (
	IntervalTimeRetryChunk = 30 * time.Second
	MaxTimesRetryChunk     = 3
)

var (
	ErrorGotCancelRequest = errors.New("got cancel request")
)

var ChunkerOpts = &chunkers.ChunkerOpts{
	MinSize:    512 * 1024,
	MaxSize:    8 * 1024 * 1024,
	NormalSize: 2 * 1024 * 1024,
}

// chunkBufPool reuses per-chunk byte buffers to reduce GC pressure during backup.
// Buffers are sized up to the chunker MaxSize.
var chunkBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, ChunkerOpts.MaxSize)
		return &b
	},
}

func getChunkBuf(size int) []byte {
	bp := chunkBufPool.Get().(*[]byte)
	b := *bp
	if cap(b) < size {
		b = make([]byte, size)
	} else {
		b = b[:size]
	}
	return b
}

func putChunkBuf(b []byte) {
	if cap(b) == 0 {
		return
	}
	bb := b[:0]
	chunkBufPool.Put(&bb)
}

// md5Pool reuses md5 hashers per chunk to avoid per-call allocation.
var md5Pool = sync.Pool{
	New: func() interface{} { return md5.New() },
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

func (c *Client) backupChunk(ctx context.Context, data []byte, chunk *cache.ChunkInfo, cacheWriter *cache.Repository, storageVault storage_vault.StorageVault, pipe chan<- *cache.Chunk, rpID, bdID string) (uint64, error) {
	select {
	case <-ctx.Done():
		return 0, ErrorGotCancelRequest
	default:
		var stat uint64

		h := md5Pool.Get().(hash.Hash)
		h.Reset()
		h.Write(data)
		sum := h.Sum(nil)
		md5Pool.Put(h)
		key := hex.EncodeToString(sum)
		chunk.Etag = key

		chunks := cache.NewChunk(bdID, rpID)
		chunks.Chunks[key] = []string{strconv.Itoa(1), strconv.Itoa(int(chunk.Length))}

		err := c.PutObject(storageVault, key, data)
		if err != nil {
			c.logger.Error("err put object", zap.Error(err), zap.String("rp_id", rpID), zap.String("bd_id", bdID))
			return stat, err
		}

		pipe <- chunks
		stat += uint64(chunk.Length)
		return stat, nil
	}
}

func (c *Client) OpenFile(ctx context.Context, path string) (io.ReadCloser, error) {
	file, err := os.Open(path)
	// Try to create vss snapshot of file to back up if open error
	if err != nil && viper.GetBool("force") {
		if errPrivileges := vss.HasSufficientPrivilegesForVSS(); errPrivileges == nil {
			errorHandler := func(item string, err error) error {
				c.logger.Error("Create VSS snapshot error: ", zap.Error(err))
				return err
			}

			messageHandler := func(msg string, args ...interface{}) {
				c.logger.Sugar().Infof(msg, args)
			}

			localVss := vss.NewLocalVss(errorHandler, messageHandler)
			defer localVss.DeleteSnapshots()
			file, err = os.Open(localVss.SnapshotPath(path))
		}
	}

	return file, err
}

func (c *Client) ChunkFileToBackup(ctx context.Context, pool *ants.Pool, itemInfo *cache.Node, cacheWriter *cache.Repository,
	storageVault storage_vault.StorageVault, p *progress.Progress, pipe chan<- *cache.Chunk, rpID, bdID string) (uint64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	select {
	case <-ctx.Done():
		return 0, ErrorGotCancelRequest
	default:
		s := progress.Stat{}

		var errBackupChunk error
		var wg sync.WaitGroup
		var stat uint64
		var chunkData []byte
		var chunkStart uint
		var fileHash hash.Hash
		var errChunk error

		bo := backoff.WithMaxRetries(backoff.NewConstantBackOff(IntervalTimeRetryChunk), MaxTimesRetryChunk)

		for {
			// chunkOnce wraps a single open+chunk attempt so file.Close happens per iteration,
			// avoiding defer-in-loop FD leaks across retries.
			loopErr := func() error {
				file, err := c.OpenFile(ctx, itemInfo.AbsolutePath)
				if err != nil {
					if os.IsNotExist(err) {
						s.ItemName = append(s.ItemName, itemInfo.AbsolutePath)
						s.Errors = true
						p.Report(s)
						return nil
					}
					c.logger.Error("err ", zap.Error(err), zap.String("rp_id", rpID), zap.String("bd_id", bdID))
					return err
				}
				defer file.Close()

				chk, err := chunkers.NewChunker("fastcdc-v1.0.0", file, ChunkerOpts)
				if err != nil {
					c.logger.Error("new chunker err", zap.Error(err), zap.String("rp_id", rpID), zap.String("bd_id", bdID))
					return err
				}
				fileHash = sha256.New()
				chunkStart = 0
				for {
					chunkData, err = chk.Next()
					if err != nil && err != io.EOF {
						c.logger.Error("next chunk err ", zap.Error(err), zap.String("rp_id", rpID), zap.String("bd_id", bdID))
						return err
					}

					if len(chunkData) == 0 {
						if err == io.EOF {
							return io.EOF
						}
						continue
					}

					temp := getChunkBuf(len(chunkData))
					length := copy(temp, chunkData)
					if uint(length) != uint(len(chunkData)) {
						c.logger.Error("compare error: ", zap.Uint("length", uint(length)), zap.Uint("chunk length", uint(len(chunkData))), zap.String("rp_id", rpID), zap.String("bd_id", bdID))
						c.logger.Sugar().Errorf("compare error when chunk file %s (rp_id=%s bd_id=%s)", itemInfo.AbsolutePath, rpID, bdID)
						putChunkBuf(temp)
						return errors.New("copy chunk data error")
					}
					chunkToBackup := cache.ChunkInfo{
						Start:  chunkStart,
						Length: uint(len(chunkData)),
					}
					chunkStart += uint(length)
					fileHash.Write(temp[:length])
					itemInfo.Content = append(itemInfo.Content, &chunkToBackup)
					wg.Add(1)
					_ = pool.Submit(c.backupChunkJob(ctx, cancel, &wg, &errBackupChunk, &stat, temp, &chunkToBackup, cacheWriter, storageVault, p, pipe, rpID, bdID))

					if err == io.EOF {
						return io.EOF
					}
				}
			}()

			if loopErr == nil || loopErr == io.EOF {
				break
			}
			d := bo.NextBackOff()
			if d == backoff.Stop {
				c.logger.Sugar().Debugf("chunk file error: %s, Retry time out (rp_id=%s bd_id=%s)", loopErr, rpID, bdID)
				errChunk = loopErr
				break
			}
			c.logger.Sugar().Errorf("chunk file error: %s, retrying... (rp_id=%s bd_id=%s)", loopErr, rpID, bdID)
		}
		wg.Wait()

		if errChunk != nil {
			return 0, errChunk
		}

		if errBackupChunk != nil {
			c.logger.Error("err backup chunk ", zap.Error(errBackupChunk), zap.String("rp_id", rpID), zap.String("bd_id", bdID))
			return 0, errBackupChunk
		}
		itemInfo.Sha256Hash = fileHash.Sum(nil)
		return stat, nil
	}
}

type chunkJob func()

func (c *Client) backupChunkJob(ctx context.Context, cancel context.CancelFunc, wg *sync.WaitGroup, chErr *error, size *uint64,
	data []byte, chunk *cache.ChunkInfo, cacheWriter *cache.Repository, storageVault storage_vault.StorageVault, p *progress.Progress, pipe chan<- *cache.Chunk, rpID, bdID string) chunkJob {
	return func() {
		defer func() {
			if r := recover(); r != nil {
				c.logger.Sugar().Errorf("backupChunkJob panic recovered: %v (rp_id=%s bd_id=%s)", r, rpID, bdID)
				*chErr = fmt.Errorf("backupChunkJob panic: %v", r)
				cancel()
			}
			putChunkBuf(data)
			wg.Done()
		}()

		select {
		case <-ctx.Done():
			return
		default:
			s := progress.Stat{}
			saveSize, err := c.backupChunk(ctx, data, chunk, cacheWriter, storageVault, pipe, rpID, bdID)
			if err != nil {
				c.logger.Error("backupChunk err ", zap.Error(err), zap.String("rp_id", rpID), zap.String("bd_id", bdID))
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
		return 0, ErrorGotCancelRequest
	default:
		s := progress.Stat{}

		// backup item with item change mtime
		if lastInfo == nil || !strings.EqualFold(timeToString(lastInfo.ModTime), timeToString(itemInfo.ModTime)) {
			storageSize, err := c.ChunkFileToBackup(ctx, pool, itemInfo, cacheWriter, storageVault, p, pipe, rpID, bdID)
			if err != nil {
				c.logger.Error("c.ChunkFileToBackup ", zap.Error(err), zap.String("rp_id", rpID), zap.String("bd_id", bdID))
				s.Errors = true
				p.Report(s)
				return 0, err
			}
			p.Report(s)
			return storageSize, nil
		} else {
			for _, content := range lastInfo.Content {
				chunks := cache.NewChunk(bdID, rpID)
				chunks.Chunks[content.Etag] = []string{strconv.Itoa(1), strconv.Itoa(int(content.Length))}
				pipe <- chunks
			}

			itemInfo.Content = lastInfo.Content
			itemInfo.Sha256Hash = lastInfo.Sha256Hash
		}
		p.Report(s)
		return 0, nil
	}
}

func (c *Client) RestoreDirectory(ctx context.Context, index cache.Index, destDir string, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, p *progress.Progress) error {
	s := progress.Stat{}
	numGoroutine := viper.GetInt("num_goroutine")
	if numGoroutine == 0 {
		numGoroutine = int(float64(runtime.NumCPU()) * 0.2)
		if numGoroutine <= 1 {
			numGoroutine = 2
		}
	}
	sem := semaphore.NewWeighted(int64(numGoroutine))
	group, ctx := errgroup.WithContext(ctx)

	for _, item := range index.Items {
		select {
		case <-ctx.Done():
			p.Cancel()
			break
		default:
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
	}

	if err := group.Wait(); err != nil {
		c.logger.Error("Has a goroutine error ", zap.Error(err))
		return err
	}
	return nil
}

func (c *Client) RestoreItem(ctx context.Context, destDir string, item cache.Node, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, p *progress.Progress) error {
	select {
	case <-ctx.Done():
		return ErrorGotCancelRequest
	default:
		s := progress.Stat{}
		var pathItem string
		if destDir == item.BasePath {
			pathItem = item.AbsolutePath
		} else {
			pathItem = filepath.Join(destDir, item.RelativePath)
		}
		switch item.Type {
		case "symlink":
			err := c.restoreSymlink(ctx, pathItem, item, p)
			if err != nil {
				c.logger.Error("Error restore symlink ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
			p.Report(s)
		case "dir":
			err := c.restoreDirectory(ctx, pathItem, item, p)
			if err != nil {
				c.logger.Error("Error restore directory ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
			p.Report(s)
		case "file":
			err := c.restoreFile(ctx, pathItem, item, storageVault, restoreKey, p)
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

func (c *Client) restoreSymlink(ctx context.Context, target string, item cache.Node, p *progress.Progress) error {
	select {
	case <-ctx.Done():
		return errors.New("context restore item done")
	default:
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
}

func (c *Client) restoreDirectory(ctx context.Context, target string, item cache.Node, p *progress.Progress) error {
	select {
	case <-ctx.Done():
		return nil
	default:
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
}

func (c *Client) restoreFile(ctx context.Context, target string, item cache.Node, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, p *progress.Progress) error {
	select {
	case <-ctx.Done():
		return ErrorGotCancelRequest
	default:
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

				err = c.downloadFile(ctx, file, item, storageVault, restoreKey, p)
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

				err = c.downloadFile(ctx, file, item, storageVault, restoreKey, p)
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
}

func (c *Client) downloadFile(ctx context.Context, file *os.File, item cache.Node, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, p *progress.Progress) error {
	numGoroutine := viper.GetInt("num_goroutine")
	if numGoroutine <= 0 {
		numGoroutine = 4
	}

	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, numGoroutine)
	for _, info := range item.Content {
		info := info
		select {
		case <-gctx.Done():
			return gctx.Err()
		case sem <- struct{}{}:
		}
		g.Go(func() error {
			defer func() { <-sem }()
			select {
			case <-gctx.Done():
				return ErrorGotCancelRequest
			default:
			}
			s := progress.Stat{}
			offset := int64(info.Start)
			sw := &sectionWriter{f: file, off: offset}
			n, err := c.GetObjectTo(storageVault, info.Etag, restoreKey, sw)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				s.Errors = true
				p.Report(s)
				return err
			}
			s.Bytes = uint64(n)
			s.Storage = uint64(n)
			p.Report(s)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	s := progress.Stat{}
	if err := os.Chmod(file.Name(), item.Mode); err != nil {
		c.logger.Error("err ", zap.Error(err))
		s.Errors = true
		p.Report(s)
		return err
	}
	_ = support.SetChownItem(file.Name(), int(item.UID), int(item.GID))
	if err := os.Chtimes(file.Name(), item.AccessTime, item.ModTime); err != nil {
		c.logger.Error("err ", zap.Error(err))
		s.Errors = true
		p.Report(s)
		return err
	}
	return nil
}

// sectionWriter adapts *os.File + offset to io.Writer using WriteAt so downloadFile can
// stream each chunk to its correct file offset without buffering the whole chunk.
type sectionWriter struct {
	f   *os.File
	off int64
}

func (s *sectionWriter) Write(p []byte) (int, error) {
	n, err := s.f.WriteAt(p, s.off)
	s.off += int64(n)
	return n, err
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

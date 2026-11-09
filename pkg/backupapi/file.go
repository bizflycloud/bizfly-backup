package backupapi

import (
	"bytes"
	"context"
	"crypto/md5"
	cryptorand "crypto/rand"
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
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

		err := c.PutObject(ctx, storageVault, key, data)
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
	// parent is the caller's context. ctx below is local: chunk jobs cancel it themselves
	// when an upload fails, so only parent tells a real cancellation apart from that.
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	select {
	case <-ctx.Done():
		return 0, ErrorGotCancelRequest
	default:
		s := progress.Stat{}

		var res chunkResults
		var wg sync.WaitGroup
		var chunkData []byte
		var chunkStart uint
		var fileHash hash.Hash
		var errChunk error

		bo := backoff.WithMaxRetries(backoff.NewConstantBackOff(IntervalTimeRetryChunk), MaxTimesRetryChunk)

		vanished := false
		for {
			// Every attempt re-chunks the file from byte 0, so it must also start a fresh
			// chunk list: keeping the chunks of a failed attempt would list them twice in
			// the index (chunks already submitted by that attempt are still uploaded and
			// simply stay unreferenced).
			itemInfo.Content = nil
			fileHash = nil

			// chunkOnce wraps a single open+chunk attempt so file.Close happens per iteration,
			// avoiding defer-in-loop FD leaks across retries.
			loopErr := func() error {
				file, err := c.OpenFile(ctx, itemInfo.AbsolutePath)
				if err != nil {
					if os.IsNotExist(err) {
						s.ItemName = append(s.ItemName, itemInfo.AbsolutePath)
						s.Errors = true
						p.Report(s)
						vanished = true
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
					_ = pool.Submit(c.backupChunkJob(ctx, cancel, &wg, &res, temp, &chunkToBackup, cacheWriter, storageVault, p, pipe, rpID, bdID))

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
		// A failed chunk job records its error before cancelling ctx, so the real upload
		// error is returned here rather than a cancellation.
		stat, errBackupChunk := res.result()
		if errBackupChunk != nil {
			c.logger.Error("err backup chunk ", zap.Error(errBackupChunk), zap.String("rp_id", rpID), zap.String("bd_id", bdID))
			return 0, errBackupChunk
		}
		// Cancelled by the caller: chunk jobs skipped their upload and left chunks without
		// an Etag, so the entry must not be reported as backed up.
		if parent.Err() != nil {
			return 0, ErrorGotCancelRequest
		}
		if vanished || fileHash == nil {
			// The file disappeared after the scan: nothing of it is backed up. Record it
			// as empty instead of leaving a stale hash (fileHash is nil on a first attempt,
			// and calling Sum on it used to panic).
			c.logger.Sugar().Infof("file vanished before backup, recorded as empty: %s (rp_id=%s bd_id=%s)", itemInfo.AbsolutePath, rpID, bdID)
			itemInfo.Content = nil
			itemInfo.Sha256Hash = nil
			return stat, nil
		}
		itemInfo.Sha256Hash = fileHash.Sum(nil)
		return stat, nil
	}
}

type chunkJob func()

// chunkResults collects the outcome of a file's chunk jobs, which run concurrently.
type chunkResults struct {
	mu   sync.Mutex
	err  error
	size uint64
}

// fail records err unless an error is already recorded: the first failure is the cause,
// later ones are usually its consequence (jobs cancelled because of it).
func (r *chunkResults) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
}

func (r *chunkResults) add(n uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.size += n
}

func (r *chunkResults) result() (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size, r.err
}

func (c *Client) backupChunkJob(ctx context.Context, cancel context.CancelFunc, wg *sync.WaitGroup, res *chunkResults,
	data []byte, chunk *cache.ChunkInfo, cacheWriter *cache.Repository, storageVault storage_vault.StorageVault, p *progress.Progress, pipe chan<- *cache.Chunk, rpID, bdID string) chunkJob {
	return func() {
		defer func() {
			if r := recover(); r != nil {
				c.logger.Sugar().Errorf("backupChunkJob panic recovered: %v (rp_id=%s bd_id=%s)", r, rpID, bdID)
				res.fail(fmt.Errorf("backupChunkJob panic: %v", r))
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
				if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, ErrorGotCancelRequest)) {
					// Interrupted because another chunk failed or the backup was stopped:
					// not a failure of its own, and must not hide the real cause.
					return
				}
				c.logger.Error("backupChunk err ", zap.Error(err), zap.String("rp_id", rpID), zap.String("bd_id", bdID))
				res.fail(err)
				s.Errors = true
				p.Report(s)
				cancel()
				return
			}
			s.Storage = saveSize
			s.Bytes = uint64(chunk.Length)
			p.Report(s)
			res.add(saveSize)
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
	numGoroutine := viper.GetInt("num_goroutine")
	if numGoroutine == 0 {
		numGoroutine = int(float64(runtime.NumCPU()) * 0.2)
		if numGoroutine <= 1 {
			numGoroutine = 2
		}
	}
	sem := semaphore.NewWeighted(int64(numGoroutine))
	parent := ctx
	group, ctx := errgroup.WithContext(ctx)

schedule:
	for _, item := range index.Items {
		select {
		case <-ctx.Done():
			// Stop scheduling items (a plain break only left the select); the items
			// already started finish before group.Wait returns.
			p.Cancel()
			break schedule
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
					p.Report(progress.Stat{Errors: true})
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
	if parent.Err() != nil {
		// Cancelled before any item failed: items were skipped, the restore is incomplete.
		return ErrorGotCancelRequest
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
		if fi.IsDir() {
			// A directory that already existed may hold temporary files from a restore
			// that was killed; a newly created one cannot.
			c.removeStaleRestoreTemps(target)
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
				if err := c.restoreFileContent(ctx, target, item, storageVault, restoreKey, p); err != nil {
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
				// restoreFileContent replaces target only once the new content is complete,
				// so a failed download leaves the existing file untouched.
				if err := c.restoreFileContent(ctx, target, item, storageVault, restoreKey, p); err != nil {
					s.Errors = true
					p.Report(s)
					return err
				}
				return nil
			} else {
				c.logger.Sugar().Info("file change ctime. update mode, uid, gid ", target)
				if err := c.applyRestoredMetadata(target, item); err != nil {
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

// restoreFileContent downloads every chunk of item into a temporary file next to target,
// closes it and then renames it over target. A failed or cancelled download never leaves a
// partially written file, and leaves an existing target untouched. Replacing target is
// atomic except in replaceFile's Windows fallback, described there.
func (c *Client) restoreFileContent(ctx context.Context, target string, item cache.Node, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, p *progress.Progress) (err error) {
	chunks, err := normalizeChunks(item.Content)
	if err != nil {
		c.logger.Error("invalid chunk list in index ", zap.Error(err), zap.String("path", target))
		return fmt.Errorf("%s: %w", target, err)
	}
	item.Content = chunks

	tmp, err := tempRestorePath(target)
	if err != nil {
		return err
	}
	file, err := c.createFile(tmp, item.Mode, int(item.UID), int(item.GID))
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}
	defer func() {
		// Flush the data before the rename makes it visible under target. Otherwise, after a
		// power loss, some filesystems (XFS, ext4 in some modes) can persist the rename but
		// not the data, leaving target empty or truncated. Restored metadata (mtime) can
		// survive with it, and a later restore then treats the damaged file as unchanged.
		if err == nil {
			if serr := file.Sync(); serr != nil {
				if isSyncUnsupported(serr) {
					// Some FUSE/SMB mounts do not implement fsync. Failing every file there
					// would make restore unusable; durability is then up to the mount.
					c.logger.Warn("fsync not supported, restored file not flushed ", zap.Error(serr), zap.String("path", tmp))
				} else {
					c.logger.Error("sync restored file error ", zap.Error(serr), zap.String("path", tmp))
					err = serr
				}
			}
		}
		if cerr := file.Close(); cerr != nil && err == nil {
			c.logger.Error("close restored file error ", zap.Error(cerr), zap.String("path", tmp))
			err = cerr
		}
		if err == nil {
			err = c.applyRestoredMetadata(tmp, item)
		}
		if err == nil {
			err = removeEmptyDirInTheWay(target)
		}
		if err == nil {
			err = replaceFile(tmp, target)
			if err != nil {
				c.logger.Error("replace restored file error ", zap.Error(err), zap.String("path", target))
			}
		}
		if err != nil {
			if rerr := os.Remove(tmp); rerr != nil && !os.IsNotExist(rerr) {
				c.logger.Error("remove partial file error ", zap.Error(rerr), zap.String("path", tmp))
			}
		}
	}()

	if err = c.downloadFile(ctx, file, item, storageVault, restoreKey, p); err != nil {
		c.logger.Error("downloadFile error ", zap.Error(err))
		return err
	}
	if err = verifyRestoredContent(file, item); err != nil {
		c.logger.Error("restored file failed verification ", zap.Error(err), zap.String("path", target))
		return fmt.Errorf("%s: %w", target, err)
	}
	return nil
}

// ErrInvalidChunkList is returned when an index entry's chunks do not describe one
// contiguous file: a gap, or two different chunks covering the same bytes.
var ErrInvalidChunkList = errors.New("index chunk list is not contiguous")

// ErrRestoredHashMismatch is returned when the restored content does not have the sha256
// recorded at backup time.
var ErrRestoredHashMismatch = errors.New("restored file sha256 does not match backup")

// normalizeChunks returns the chunks sorted by offset, checking that they tile the file
// from byte 0 with no gap and no overlap. Exact duplicates are dropped: an older agent
// listed the chunks of a failed chunking attempt again after retrying, and since chunking is
// deterministic those duplicates are identical and harmless. Any other overlap or a gap means
// the index cannot describe the file and restoring it would produce wrong content.
//
// Node.Size is deliberately not compared: it is taken when the directory is scanned, while
// the chunks (and Sha256Hash) reflect the data read later, so a file that changed between
// the two legitimately differs.
func normalizeChunks(in []*cache.ChunkInfo) ([]*cache.ChunkInfo, error) {
	if len(in) == 0 {
		return in, nil
	}
	sorted := make([]*cache.ChunkInfo, len(in))
	copy(sorted, in)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

	out := sorted[:0:0]
	var next uint
	for _, ch := range sorted {
		if ch == nil {
			return nil, fmt.Errorf("%w: nil chunk", ErrInvalidChunkList)
		}
		if n := len(out); n > 0 {
			prev := out[n-1]
			if ch.Start == prev.Start && ch.Length == prev.Length && ch.Etag == prev.Etag {
				continue // identical duplicate
			}
		}
		if ch.Start != next {
			if ch.Start < next {
				return nil, fmt.Errorf("%w: chunk %s at %d overlaps bytes up to %d", ErrInvalidChunkList, ch.Etag, ch.Start, next)
			}
			return nil, fmt.Errorf("%w: gap from %d to %d", ErrInvalidChunkList, next, ch.Start)
		}
		out = append(out, ch)
		next = ch.Start + ch.Length
	}
	return out, nil
}

// verifyRestoredContent re-reads the restored file and compares its sha256 with the one
// recorded at backup time. Indexes without a hash (or a file recorded as empty) are only
// checked for length. The data was just written, so it is normally read back from the page
// cache.
func verifyRestoredContent(file *os.File, item cache.Node) error {
	var want int64
	for _, ch := range item.Content {
		want += int64(ch.Length)
	}
	fi, err := file.Stat()
	if err != nil {
		return err
	}
	if fi.Size() != want {
		return fmt.Errorf("%w: size %d, chunks cover %d", ErrRestoredHashMismatch, fi.Size(), want)
	}
	if len(item.Sha256Hash) == 0 {
		return nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(file, 0, want)); err != nil {
		return fmt.Errorf("read back restored file: %w", err)
	}
	if got := h.Sum(nil); !bytes.Equal(got, item.Sha256Hash) {
		return fmt.Errorf("%w: got %x, want %x", ErrRestoredHashMismatch, got, []byte(item.Sha256Hash))
	}
	return nil
}

// isSyncUnsupported reports whether an fsync error means the filesystem does not implement
// it, as opposed to a real write-back failure.
func isSyncUnsupported(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP)
}

// removeEmptyDirInTheWay removes target if it is an empty directory, so a file can be
// restored where the destination now has an empty directory of the same name (the old
// restore removed it too). A non-empty directory is reported instead of being replaced.
func removeEmptyDirInTheWay(target string) error {
	fi, err := os.Lstat(target)
	if err != nil || !fi.IsDir() {
		return nil
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("a directory is in the way of restored file %s: %w", target, err)
	}
	return nil
}

// Temporary restore files are named restoreTempPrefix + <process token> + "-" + <random>.
// The name deliberately does not include the target's name, so it stays short (41 bytes)
// whatever the target's length: a file whose name is close to the 255-byte limit must still
// be restorable.
const restoreTempPrefix = ".bizfly-restore-"

// restoreTempToken identifies temporary files created by this agent process. Files with the
// prefix but another token were left behind by a process that died mid-restore. It is always
// 8 lowercase hex characters, as restoreTempName expects.
var restoreTempToken = func() string {
	var b [4]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()))
	}
	return hex.EncodeToString(b[:])
}()

// restoreTempName matches the full name of a temporary restore file: prefix, 8 hex token,
// "-", 16 hex random. Matching the whole pattern, not just the prefix, keeps a user's own
// file that merely starts with the prefix from being skipped by backup or deleted.
var restoreTempName = regexp.MustCompile(`^` + regexp.QuoteMeta(restoreTempPrefix) + `[0-9a-f]{8}-[0-9a-f]{16}$`)

// IsRestoreTempFile reports whether name is a temporary file created while restoring. Backups
// skip such files.
func IsRestoreTempFile(name string) bool {
	return restoreTempName.MatchString(name)
}

// tempRestorePath returns a hidden, unique path in target's directory. Keeping it in the same
// directory makes the final rename atomic on the same filesystem.
func tempRestorePath(target string) (string, error) {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", err
	}
	name := restoreTempPrefix + restoreTempToken + "-" + hex.EncodeToString(b[:])
	return filepath.Join(filepath.Dir(target), name), nil
}

// removeStaleRestoreTemps deletes temporary restore files in dir that were left by an earlier
// agent process (killed or crashed mid-restore). Files created by this process are kept: they
// may belong to a download still in progress in another goroutine.
func (c *Client) removeStaleRestoreTemps(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		c.logger.Error("list directory for stale restore files ", zap.Error(err), zap.String("dir", dir))
		return
	}
	own := restoreTempPrefix + restoreTempToken + "-"
	for _, e := range entries {
		name := e.Name()
		if !IsRestoreTempFile(name) || strings.HasPrefix(name, own) || !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			c.logger.Error("remove stale restore file ", zap.Error(err), zap.String("path", path))
			continue
		}
		c.logger.Sugar().Info("removed stale restore file ", path)
	}
}

// replaceFile renames src over dst. Rename replaces an existing file atomically on Unix and,
// usually, on Windows. Windows refuses to replace a read-only file with "access denied"; only
// in that case dst is removed first and src renamed again. That fallback is not atomic: if
// the second rename also fails, the previous version of dst is lost. Any other failure,
// e.g. dst being a directory, is returned as-is and dst is left untouched.
func replaceFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	if runtime.GOOS != "windows" || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	fi, serr := os.Lstat(dst)
	if serr != nil || !fi.Mode().IsRegular() {
		return err
	}
	if rerr := os.Remove(dst); rerr != nil {
		return err
	}
	return os.Rename(src, dst)
}

func (c *Client) downloadFile(ctx context.Context, file *os.File, item cache.Node, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, p *progress.Progress) error {
	numGoroutine := viper.GetInt("num_goroutine")
	if numGoroutine <= 0 {
		numGoroutine = 4
	}

	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, numGoroutine)
schedule:
	for _, info := range item.Content {
		info := info
		select {
		case <-gctx.Done():
			// Stop scheduling, but still wait below: chunks already started keep writing
			// into file until they return.
			break schedule
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
			n, err := c.GetObjectTo(gctx, storageVault, info.Etag, restoreKey, file, int64(info.Start), int64(info.Length))
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
	if ctx.Err() != nil {
		// Cancelled before any chunk failed: some chunks were never downloaded.
		return ErrorGotCancelRequest
	}
	return nil
}

// applyRestoredMetadata sets owner, mode and times on a closed file. It runs after the data
// has been flushed and closed, so a network filesystem that updates mtime when it flushes
// cannot overwrite the restored mtime. Owner is set before mode because chown clears the
// setuid/setgid bits on Linux.
func (c *Client) applyRestoredMetadata(path string, item cache.Node) error {
	_ = support.SetChownItem(path, int(item.UID), int(item.GID))
	if err := os.Chmod(path, item.Mode); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}
	if err := os.Chtimes(path, item.AccessTime, item.ModTime); err != nil {
		c.logger.Error("err ", zap.Error(err))
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
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}

	_ = support.SetChownItem(path, uid, gid)
	return file, nil
}

func timeToString(time time.Time) string {
	return time.Format("2006-01-02 15:04:05.000000")
}

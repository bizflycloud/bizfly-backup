package backupapi

import (
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/panjf2000/ants/v2"
	"github.com/spf13/viper"

	"github.com/bizflycloud/bizfly-backup/pkg/cache"
)

// A restore-key endpoint answering 404 (returned by Do as a response) or a body without a
// key must not replace the current, still valid session with an empty one.
func TestClient_refreshVaultCredential_BadSessionResponseKeepsSession(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			api := &restoreAPI{sessionStatus: status}
			srv := httptest.NewServer(api.handler())
			t.Cleanup(srv.Close)
			c, err := NewClient(WithServerURL(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			rk := &AuthRestore{RecoveryPointID: "rp", ActionID: "act", CreatedAt: "t0", RestoreSessionKey: "key-0"}

			if err := c.refreshVaultCredential(context.Background(), newFakeVault(), rk, 0); err == nil {
				t.Fatal("want error for an unusable session response")
			}
			if got := rk.session(); got.key != "key-0" || got.createdAt != "t0" {
				t.Fatalf("session replaced by %+v, want key-0/t0 kept", got)
			}
		})
	}
}

func TestIsSyncUnsupported(t *testing.T) {
	wrap := func(e error) error { return &fs.PathError{Op: "sync", Path: "/x", Err: e} }
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{wrap(syscall.EINVAL), true},
		{wrap(syscall.ENOTSUP), true},
		{wrap(syscall.EOPNOTSUPP), true},
		{wrap(syscall.EIO), false},
		{wrap(syscall.ENOSPC), false},
	} {
		if got := isSyncUnsupported(tc.err); got != tc.want {
			t.Errorf("isSyncUnsupported(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// Any 403 (whatever its code) and 400 InvalidToken are credential refusals: a DEFAULT vault
// refreshes and succeeds instead of failing as a permanent client error.
func TestClient_GetObjectTo_AnyForbiddenRefreshes(t *testing.T) {
	for _, denied := range []error{
		&httpAPIError{"AccountProblem", 403},
		&httpAPIError{"", 403},
		&httpAPIError{"InvalidToken", 400},
	} {
		t.Run(denied.Error(), func(t *testing.T) {
			api := &restoreAPI{}
			srv := httptest.NewServer(api.handler())
			t.Cleanup(srv.Close)
			c, err := NewClient(WithServerURL(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			vault := newFakeVault()
			vault.objects["chunk"] = pattern(10, 'f')
			vault.denyUntilRefresh = true
			vault.deniedErr = denied
			rk := &AuthRestore{RecoveryPointID: "rp", ActionID: "act", CreatedAt: "t0", RestoreSessionKey: "key-0"}

			if _, err := c.GetObjectTo(context.Background(), vault, "chunk", rk, tempFile(t), 0, 10); err != nil {
				t.Fatalf("GetObjectTo: %v", err)
			}
			if vault.refreshes != 1 {
				t.Fatalf("refreshes = %d, want 1", vault.refreshes)
			}
		})
	}
}

// After a successful refresh the download is retried at once, not after a backoff wait
// (the first backoff interval is at least 250ms).
func TestClient_GetObjectTo_RetriesImmediatelyAfterRefresh(t *testing.T) {
	api := &restoreAPI{}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	c, err := NewClient(WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	vault := newFakeVault()
	vault.objects["chunk"] = pattern(10, 'i')
	vault.denyUntilRefresh = true
	rk := &AuthRestore{RecoveryPointID: "rp", ActionID: "act", CreatedAt: "t0", RestoreSessionKey: "key-0"}

	start := time.Now()
	if _, err := c.GetObjectTo(context.Background(), vault, "chunk", rk, tempFile(t), 0, 10); err != nil {
		t.Fatalf("GetObjectTo: %v", err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("took %v, want the retry right after the refresh", d)
	}
}

// An empty directory where the restored file goes is replaced, as the old restore did; a
// non-empty one is reported and left alone.
func TestClient_restoreFile_DirectoryInTheWay(t *testing.T) {
	vault := newFakeVault()
	vault.objects["A"] = pattern(8, 'd')
	node := func() cache.Node {
		mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		return cache.Node{Mode: 0644, ModTime: mtime, AccessTime: mtime, ChangeTime: mtime,
			Content: []*cache.ChunkInfo{chunk(0, 8, "A")}}
	}
	c := newTestClient(t)

	t.Run("empty", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "x")
		if err := os.Mkdir(target, 0755); err != nil {
			t.Fatal(err)
		}
		if err := c.restoreFile(context.Background(), target, node(), vault, nil, nil); err != nil {
			t.Fatalf("restoreFile: %v", err)
		}
		if fi, err := os.Stat(target); err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("target is not the restored file: %v", err)
		}
	})

	t.Run("not empty", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "x")
		if err := os.MkdirAll(filepath.Join(target, "child"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := c.restoreFile(context.Background(), target, node(), vault, nil, nil); err == nil {
			t.Fatal("want error for a non-empty directory in the way")
		}
		if _, err := os.Stat(filepath.Join(target, "child")); err != nil {
			t.Fatalf("directory content lost: %v", err)
		}
		assertNoTempFiles(t, filepath.Dir(target))
	})
}

// Several items failing at once used to write one shared progress.Stat from every
// goroutine. Run with -race.
func TestClient_RestoreDirectory_ConcurrentFailuresNoRace(t *testing.T) {
	prev := viper.GetInt("num_goroutine")
	viper.Set("num_goroutine", 4)
	t.Cleanup(func() { viper.Set("num_goroutine", prev) })

	dir := t.TempDir()
	index := cache.Index{Items: map[string]*cache.Node{}}
	for _, name := range []string{"a", "b", "c", "d"} {
		index.Items[name] = &cache.Node{
			Type: "file", Name: name, Mode: 0644,
			AbsolutePath: filepath.Join(dir, "src", name), BasePath: filepath.Join(dir, "src"), RelativePath: name,
			Content: []*cache.ChunkInfo{chunk(0, 4, "missing-"+name)},
		}
	}
	c := newTestClient(t)
	if err := c.RestoreDirectory(context.Background(), index, filepath.Join(dir, "dst"), newFakeVault(), nil, nil); err == nil {
		t.Fatal("want error")
	}
}

// If the backup is cancelled while a file is being chunked, the remaining chunk jobs skip
// their upload and leave chunks without an Etag. The file must be reported as not backed up.
func TestClient_ChunkFileToBackup_CancelledMidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.bin")
	data := make([]byte, 24<<20) // several chunks at 2 MiB average
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	pool, err := ants.NewPool(1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vault := newFakeVault()
	vault.onPut = func(context.Context) error { cancel(); return nil } // cancel on the first upload
	pipe := make(chan *cache.Chunk, 64)

	c := newTestClient(t)
	item := &cache.Node{AbsolutePath: path}
	_, err = c.ChunkFileToBackup(ctx, pool, item, nil, vault, nil, pipe, "rp", "bd")
	if !errors.Is(err, ErrorGotCancelRequest) {
		t.Fatalf("err = %v, want ErrorGotCancelRequest", err)
	}
}

// A chunk upload that fails cancels ChunkFileToBackup's own context. That must still be
// reported as the upload error, not as a cancellation nobody requested. (The fake panics so
// the job's recover records the error at once instead of PutObject retrying for minutes.)
func TestClient_ChunkFileToBackup_UploadErrorNotReportedAsCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.bin")
	data := make([]byte, 4<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	pool, err := ants.NewPool(1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release()

	vault := newFakeVault()
	vault.onPut = func(context.Context) error { panic("upload failed") }
	c := newTestClient(t)
	_, err = c.ChunkFileToBackup(context.Background(), pool, &cache.Node{AbsolutePath: path}, nil, vault, nil, make(chan *cache.Chunk, 64), "rp", "bd")
	if err == nil || errors.Is(err, ErrorGotCancelRequest) {
		t.Fatalf("err = %v, want the upload error", err)
	}
}

// When one chunk upload fails, the uploads still running are cancelled and fail with
// context.Canceled. The reported error must stay the first, real failure.
func TestClient_ChunkFileToBackup_KeepsFirstUploadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.bin")
	data := make([]byte, 24<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	pool, err := ants.NewPool(4)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release()

	var calls atomic.Int32
	secondStarted := make(chan struct{})
	vault := newFakeVault()
	vault.onPut = func(ctx context.Context) error {
		switch calls.Add(1) {
		case 1:
			<-secondStarted
			panic("first upload failed") // recorded by the job's recover, then cancels
		case 2:
			close(secondStarted)
		}
		<-ctx.Done() // every other upload is still in flight when the first fails
		return ctx.Err()
	}
	c := newTestClient(t)
	_, err = c.ChunkFileToBackup(context.Background(), pool, &cache.Node{AbsolutePath: path}, nil, vault, nil, make(chan *cache.Chunk, 64), "rp", "bd")
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrorGotCancelRequest) {
		t.Fatalf("err = %v, want the first upload failure", err)
	}
}

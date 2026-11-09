package backupapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/spf13/viper"

	"github.com/bizflycloud/bizfly-backup/pkg/cache"
	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"
)

// fakeVault is an in-memory storage_vault.StorageVault whose GetObjectTo can be told to
// fail part-way through the copy on the first N attempts, mimicking a connection reset
// during io.Copy in the real S3 implementation.
type fakeVault struct {
	mu      sync.Mutex
	objects map[string][]byte
	// failFirst[key] = number of leading attempts that must write only partialLen bytes and
	// then return errConnReset.
	failFirst  map[string]int
	partialLen int
	// shortBy[key] trims that many bytes off the delivered body without reporting an error.
	shortBy map[string]int
	// longBy[key] appends that many junk bytes to the delivered body.
	longBy map[string]int
	// errFor[key] is returned on every call, before anything is written.
	errFor map[string]error
	// denyUntilRefresh makes every call fail with AccessDenied until RefreshCredential has
	// been called at least once, like an expired storage credential.
	denyUntilRefresh bool
	// deniedErr is returned while denied; nil means a plain AccessDenied.
	deniedErr error
	refreshes int
	// hook, if set, runs at the start of every call.
	hook  func(ctx context.Context, key string) error
	calls map[string]int
	// credType is reported by Type(); empty means "DEFAULT".
	credType string
	// onPut, if set, runs on every PutObject; a non-nil error is returned by PutObject.
	onPut func(ctx context.Context) error
}

var errConnReset = errors.New("read tcp: connection reset by peer")

func newFakeVault() *fakeVault {
	return &fakeVault{
		objects:   map[string][]byte{},
		failFirst: map[string]int{},
		shortBy:   map[string]int{},
		longBy:    map[string]int{},
		errFor:    map[string]error{},
		calls:     map[string]int{},
	}
}

func (f *fakeVault) GetObjectTo(ctx context.Context, key string, dst io.WriterAt, off int64) (int64, error) {
	f.mu.Lock()
	body, ok := f.objects[key]
	f.calls[key]++
	attempt := f.calls[key]
	fails := f.failFirst[key]
	short := f.shortBy[key]
	long := f.longBy[key]
	ferr := f.errFor[key]
	denied := f.denyUntilRefresh && f.refreshes == 0
	deniedErr := f.deniedErr
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, key); err != nil {
			return 0, err
		}
	}
	if denied {
		if deniedErr != nil {
			return 0, deniedErr
		}
		return 0, &smithy.GenericAPIError{Code: "AccessDenied", Message: "expired"}
	}
	if ferr != nil {
		return 0, ferr
	}
	if !ok {
		return 0, &smithy.GenericAPIError{Code: "NoSuchKey"}
	}
	w := io.NewOffsetWriter(dst, off)
	if attempt <= fails {
		n, err := w.Write(body[:f.partialLen])
		if err != nil {
			return int64(n), err
		}
		return int64(n), errConnReset
	}
	if short > 0 {
		body = body[:len(body)-short]
	}
	if long > 0 {
		body = append(append([]byte(nil), body...), bytes.Repeat([]byte{'!'}, long)...)
	}
	n, err := w.Write(body)
	return int64(n), err
}

func (f *fakeVault) HeadObject(_ context.Context, key string) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok, key, nil
}
func (f *fakeVault) PutObject(ctx context.Context, key string, data []byte) error {
	if f.onPut != nil {
		if err := f.onPut(ctx); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = append([]byte(nil), data...)
	return nil
}
func (f *fakeVault) GetObject(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[key]++
	if err := f.errFor[key]; err != nil {
		return nil, err
	}
	b, ok := f.objects[key]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey"}
	}
	return append([]byte(nil), b...), nil
}
func (f *fakeVault) RefreshCredential(storage_vault.Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	return nil
}
func (f *fakeVault) ID() (string, string) { return "vault", "action" }
func (f *fakeVault) Type() storage_vault.Type {
	ct := f.credType
	if ct == "" {
		ct = "DEFAULT"
	}
	return storage_vault.Type{StorageVaultType: "S3", CredentialType: ct}
}

func (f *fakeVault) callCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[key]
}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func pattern(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i%7)
	}
	return b
}

func tempFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "restore.bin"))
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func readAll(t *testing.T, f *os.File) []byte {
	t.Helper()
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	return b
}

// Regression for the restore corruption bug: a copy that breaks mid-stream used to be
// retried against a writer whose offset had already advanced, so the retried bytes landed
// at start+n and the call still returned success. Every retry must now restart at off.
func TestClient_GetObjectTo_RetryAfterPartialWriteRestartsAtOffset(t *testing.T) {
	const off = int64(100)
	chunk := pattern(1000, 'a')

	vault := newFakeVault()
	vault.objects["chunk"] = chunk
	vault.failFirst["chunk"] = 1
	vault.partialLen = 400

	f := tempFile(t)
	// Pre-fill the region before off so we can prove it is untouched.
	prefix := pattern(int(off), 'P')
	if _, err := f.WriteAt(prefix, 0); err != nil {
		t.Fatal(err)
	}

	c := newTestClient(t)
	n, err := c.GetObjectTo(context.Background(), vault, "chunk", nil, f, off, int64(len(chunk)))
	if err != nil {
		t.Fatalf("GetObjectTo: %v", err)
	}
	if n != int64(len(chunk)) {
		t.Fatalf("n = %d, want %d", n, len(chunk))
	}
	if got := vault.callCount("chunk"); got != 2 {
		t.Fatalf("vault calls = %d, want 2 (one failure + one retry)", got)
	}

	want := append(append([]byte(nil), prefix...), chunk...)
	got := readAll(t, f)
	if !bytes.Equal(got, want) {
		t.Fatalf("file content mismatch after retry:\n len got=%d want=%d\n first diff at %d",
			len(got), len(want), firstDiff(got, want))
	}
}

// Even when the backend reports no error, a body shorter than the index length must not
// be treated as a successful restore.
func TestClient_GetObjectTo_LengthMismatchIsError(t *testing.T) {
	chunk := pattern(512, 'b')
	vault := newFakeVault()
	vault.objects["chunk"] = chunk
	vault.shortBy["chunk"] = 10

	f := tempFile(t)
	c := newTestClient(t)
	n, err := c.GetObjectTo(context.Background(), vault, "chunk", nil, f, 0, int64(len(chunk)))
	if !errors.Is(err, ErrObjectSizeMismatch) {
		t.Fatalf("err = %v, want ErrObjectSizeMismatch", err)
	}
	if n != int64(len(chunk)-10) {
		t.Fatalf("n = %d, want %d", n, len(chunk)-10)
	}
}

// wantLen < 0 disables the length check so callers without a known size still work.
func TestClient_GetObjectTo_UnknownLengthSkipsCheck(t *testing.T) {
	chunk := pattern(64, 'c')
	vault := newFakeVault()
	vault.objects["chunk"] = chunk
	vault.shortBy["chunk"] = 4

	f := tempFile(t)
	c := newTestClient(t)
	n, err := c.GetObjectTo(context.Background(), vault, "chunk", nil, f, 0, -1)
	if err != nil {
		t.Fatalf("GetObjectTo: %v", err)
	}
	if n != int64(len(chunk)-4) {
		t.Fatalf("n = %d, want %d", n, len(chunk)-4)
	}
}

// End-to-end through downloadFile: several chunks written concurrently to their offsets,
// with one of them breaking mid-stream on the first attempt, must reassemble to exactly
// the original file.
func TestClient_downloadFile_ReassemblesChunksAfterPartialFailure(t *testing.T) {
	chunks := [][]byte{pattern(300, 'x'), pattern(500, 'y'), pattern(200, 'z')}
	vault := newFakeVault()
	vault.partialLen = 100
	var want []byte
	node := cache.Node{Mode: 0644}
	var start uint
	for i, ch := range chunks {
		key := string(rune('A' + i))
		vault.objects[key] = ch
		node.Content = append(node.Content, &cache.ChunkInfo{Start: start, Length: uint(len(ch)), Etag: key})
		start += uint(len(ch))
		want = append(want, ch...)
	}
	vault.failFirst["B"] = 1 // middle chunk fails once part-way through

	f := tempFile(t)
	c := newTestClient(t)
	if err := c.downloadFile(context.Background(), f, node, vault, nil, nil); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	got := readAll(t, f)
	if !bytes.Equal(got, want) {
		t.Fatalf("restored file differs from original: len got=%d want=%d, first diff at %d",
			len(got), len(want), firstDiff(got, want))
	}
	if vault.callCount("B") != 2 {
		t.Fatalf("chunk B calls = %d, want 2", vault.callCount("B"))
	}
}

// A chunk whose delivered length never matches the index must fail the whole file.
func TestClient_downloadFile_ShortChunkFailsRestore(t *testing.T) {
	vault := newFakeVault()
	vault.objects["A"] = pattern(256, 'q')
	vault.shortBy["A"] = 1
	node := cache.Node{Mode: 0644, Content: []*cache.ChunkInfo{{Start: 0, Length: 256, Etag: "A"}}}

	f := tempFile(t)
	c := newTestClient(t)
	err := c.downloadFile(context.Background(), f, node, vault, nil, nil)
	if !errors.Is(err, ErrObjectSizeMismatch) {
		t.Fatalf("downloadFile err = %v, want ErrObjectSizeMismatch", err)
	}
}

func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}

// An object longer than its index entry must not spill into the next chunk's bytes.
func TestClient_GetObjectTo_LongerObjectDoesNotOverwriteNeighbour(t *testing.T) {
	const off = int64(10)
	chunk := pattern(100, 'l')
	vault := newFakeVault()
	vault.objects["chunk"] = chunk
	vault.longBy["chunk"] = 50

	f := tempFile(t)
	neighbour := pattern(80, 'N')
	if _, err := f.WriteAt(neighbour, off+int64(len(chunk))); err != nil {
		t.Fatal(err)
	}

	c := newTestClient(t)
	_, err := c.GetObjectTo(context.Background(), vault, "chunk", nil, f, off, int64(len(chunk)))
	if !errors.Is(err, ErrObjectSizeMismatch) {
		t.Fatalf("err = %v, want ErrObjectSizeMismatch", err)
	}
	if got := vault.callCount("chunk"); got != 1 {
		t.Fatalf("vault calls = %d, want 1 (size mismatch is not retryable)", got)
	}
	got := readAll(t, f)
	if !bytes.Equal(got[off:off+int64(len(chunk))], chunk) {
		t.Fatalf("chunk region not written correctly")
	}
	if !bytes.Equal(got[off+int64(len(chunk)):], neighbour) {
		t.Fatalf("neighbouring chunk was overwritten")
	}
}

// Errors that cannot succeed on retry must return at once instead of retrying for minutes.
func TestClient_GetObjectTo_NonRetryableErrorsReturnImmediately(t *testing.T) {
	c := newTestClient(t)

	t.Run("NoSuchKey", func(t *testing.T) {
		vault := newFakeVault()
		start := time.Now()
		_, err := c.GetObjectTo(context.Background(), vault, "missing", nil, tempFile(t), 0, 10)
		if err == nil {
			t.Fatal("want error")
		}
		if got := vault.callCount("missing"); got != 1 {
			t.Fatalf("calls = %d, want 1", got)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("took %v, want immediate return", d)
		}
	})

	t.Run("local write error", func(t *testing.T) {
		vault := newFakeVault()
		vault.objects["chunk"] = pattern(10, 'w')
		f := tempFile(t)
		_ = f.Close() // writes now fail with "file already closed"
		_, err := c.GetObjectTo(context.Background(), vault, "chunk", nil, f, 0, 10)
		var we *localWriteError
		if !errors.As(err, &we) {
			t.Fatalf("err = %v, want localWriteError", err)
		}
		if got := vault.callCount("chunk"); got != 1 {
			t.Fatalf("calls = %d, want 1", got)
		}
	})
}

// Cancelling the restore must interrupt the backoff wait instead of sleeping it out.
func TestClient_GetObjectTo_CancelInterruptsBackoff(t *testing.T) {
	vault := newFakeVault()
	vault.errFor["chunk"] = errConnReset // retryable, fails forever

	// The first backoff wait is 250-750ms; cancelling at 100ms lands inside it, so a wait
	// that ignores ctx would return at least 150ms after cancel.
	ctx, cancel := context.WithCancel(context.Background())
	var cancelledAt atomic.Int64
	time.AfterFunc(100*time.Millisecond, func() {
		cancelledAt.Store(time.Now().UnixNano())
		cancel()
	})

	c := newTestClient(t)
	_, err := c.GetObjectTo(ctx, vault, "chunk", nil, tempFile(t), 0, 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if lag := time.Since(time.Unix(0, cancelledAt.Load())); lag > 100*time.Millisecond {
		t.Fatalf("returned %v after cancel, want the backoff wait to be interrupted", lag)
	}
}

// restoreAPI is a fake backup API serving the restore session key and vault credential
// endpoints, counting how often each is hit.
type restoreAPI struct {
	sessionHits    atomic.Int32
	credentialHits atomic.Int32
	// sawStaleKey is set if a credential request carried a session key other than the
	// latest one issued.
	sawStaleKey atomic.Bool
	mu          sync.Mutex
	latestKey   string
	// prefix is the path ServerURL carries, e.g. "/api/v1".
	prefix string
	// credentialGate, if set, runs before the credential endpoint answers.
	credentialGate func(r *http.Request)
	// rejectStale makes the credential endpoint answer 401 to any session key other than
	// the latest one issued, like an expired session.
	rejectStale bool
	// reject401Once makes the first credential request answer 401 whatever the key.
	reject401Once atomic.Bool
	// sessionStatus, if non-zero, is the status the restore-key endpoint answers with.
	sessionStatus int
}

func (a *restoreAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(a.prefix+"/agent/recovery-points/rp/restore-key", func(w http.ResponseWriter, r *http.Request) {
		n := a.sessionHits.Add(1)
		if a.sessionStatus != 0 {
			w.WriteHeader(a.sessionStatus)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		key := fmt.Sprintf("key-%d", n)
		a.mu.Lock()
		a.latestKey = key
		a.mu.Unlock()
		_ = json.NewEncoder(w).Encode(RestoreResponse{CreatedAt: fmt.Sprintf("t%d", n), RestoreSessionKey: key})
	})
	mux.HandleFunc(a.prefix+"/agent/storage_vaults/vault/credential", func(w http.ResponseWriter, r *http.Request) {
		a.credentialHits.Add(1)
		if a.reject401Once.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if a.credentialGate != nil {
			a.credentialGate(r)
		}
		a.mu.Lock()
		stale := r.Header.Get("X-Restore-Session-Key") != a.latestKey
		a.mu.Unlock()
		if stale {
			a.sawStaleKey.Store(true)
			if a.rejectStale {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(StorageVault{ID: "vault"})
	})
	return mux
}

// When the credential expires, every in-flight chunk download gets AccessDenied at once.
// They must share one session/credential refresh instead of racing on restoreKey. Run with
// -race to also check there is no unsynchronised access to restoreKey.
func TestClient_GetObjectTo_ConcurrentAccessDeniedRefreshesOnce(t *testing.T) {
	api := &restoreAPI{}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)

	c, err := NewClient(WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	vault := newFakeVault()
	vault.denyUntilRefresh = true
	// Hold every worker until all have issued their first request, so they all see the
	// expired credential together.
	var arrived sync.WaitGroup
	arrived.Add(workers)
	release := make(chan struct{})
	var first sync.Map
	vault.hook = func(ctx context.Context, key string) error {
		if _, loaded := first.LoadOrStore(key, true); !loaded {
			arrived.Done()
			<-release
		}
		return nil
	}
	for i := 0; i < workers; i++ {
		vault.objects[fmt.Sprintf("k%d", i)] = pattern(64, byte('a'+i))
	}
	restoreKey := &AuthRestore{RecoveryPointID: "rp", ActionID: "act", CreatedAt: "t0", RestoreSessionKey: "key-0"}

	f := tempFile(t)
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.GetObjectTo(context.Background(), vault, fmt.Sprintf("k%d", i), restoreKey, f, int64(i*64), 64)
		}(i)
	}
	arrived.Wait()
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	if got := api.sessionHits.Load(); got != 1 {
		t.Errorf("restore session key requested %d times, want 1", got)
	}
	if got := api.credentialHits.Load(); got != 1 {
		t.Errorf("vault credential requested %d times, want 1", got)
	}
	if vault.refreshes != 1 {
		t.Errorf("RefreshCredential called %d times, want 1", vault.refreshes)
	}
	if api.sawStaleKey.Load() {
		t.Errorf("credential request used a stale restore session key")
	}
	if restoreKey.RestoreSessionKey != "key-1" || restoreKey.CreatedAt != "t1" {
		t.Errorf("restoreKey = %q/%q, want key-1/t1", restoreKey.RestoreSessionKey, restoreKey.CreatedAt)
	}
}

// After cancellation downloadFile must wait for chunks already in flight; returning early
// let them keep writing into a file the caller has moved on from.
func TestClient_downloadFile_CancelWaitsForInFlightChunks(t *testing.T) {
	prev := viper.GetInt("num_goroutine")
	viper.Set("num_goroutine", 1)
	t.Cleanup(func() { viper.Set("num_goroutine", prev) })

	vault := newFakeVault()
	vault.objects["A"] = pattern(10, 'a')
	vault.objects["B"] = pattern(10, 'b')
	started := make(chan struct{})
	var finished atomic.Bool
	vault.hook = func(ctx context.Context, key string) error {
		if key != "A" {
			return nil
		}
		close(started)
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond) // still busy after cancellation
		finished.Store(true)
		return ctx.Err()
	}
	node := cache.Node{Mode: 0644, Content: []*cache.ChunkInfo{
		{Start: 0, Length: 10, Etag: "A"},
		{Start: 10, Length: 10, Etag: "B"},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()

	c := newTestClient(t)
	err := c.downloadFile(ctx, tempFile(t), node, vault, nil, nil)
	if err == nil {
		t.Fatal("want error after cancel")
	}
	if !finished.Load() {
		t.Fatal("downloadFile returned while chunk A was still running")
	}
	if got := vault.callCount("B"); got != 0 {
		t.Fatalf("chunk B started %d times after cancel, want 0", got)
	}
}

// A failed restore must not leave a partially written file behind.
func TestClient_restoreFile_RemovesPartialFileOnError(t *testing.T) {
	vault := newFakeVault()
	vault.objects["A"] = pattern(10, 'a')
	// chunk B is missing -> NoSuchKey
	node := cache.Node{Mode: 0644, Content: []*cache.ChunkInfo{
		{Start: 0, Length: 10, Etag: "A"},
		{Start: 10, Length: 10, Etag: "B"},
	}}
	target := filepath.Join(t.TempDir(), "sub", "file.bin")

	c := newTestClient(t)
	if err := c.restoreFile(context.Background(), target, node, vault, nil, nil); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("partial file still present: stat err = %v", err)
	}
	assertNoTempFiles(t, filepath.Dir(target))
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if IsRestoreTempFile(e.Name()) {
			t.Errorf("temporary restore file left behind: %s", e.Name())
		}
	}
}

// writeChangedTarget creates target with old content and returns a node whose metadata
// differs from it, so restoreFile takes the "file changed" branch and re-downloads.
func writeChangedTarget(t *testing.T, target string, old []byte, content []*cache.ChunkInfo) cache.Node {
	t.Helper()
	if err := os.WriteFile(target, old, 0644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	return cache.Node{
		Mode:       0640,
		ModTime:    mtime,
		AccessTime: mtime,
		ChangeTime: mtime,
		Content:    content,
	}
}

// A changed file whose new version fails to download must keep its old content: the old
// file is only replaced once the new one is complete.
func TestClient_restoreFile_ChangedFileKeptOnFailure(t *testing.T) {
	vault := newFakeVault()
	vault.objects["A"] = pattern(10, 'a')
	target := filepath.Join(t.TempDir(), "file.bin")
	old := []byte("previous version")
	node := writeChangedTarget(t, target, old, []*cache.ChunkInfo{
		{Start: 0, Length: 10, Etag: "A"},
		{Start: 10, Length: 10, Etag: "missing"},
	})

	c := newTestClient(t)
	if err := c.restoreFile(context.Background(), target, node, vault, nil, nil); err == nil {
		t.Fatal("want error")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("old file gone: %v", err)
	}
	if !bytes.Equal(got, old) {
		t.Fatalf("old file content changed to %q", got)
	}
	assertNoTempFiles(t, filepath.Dir(target))
}

// A changed file restored successfully is replaced with the new content and metadata.
func TestClient_restoreFile_ChangedFileReplaced(t *testing.T) {
	vault := newFakeVault()
	vault.objects["A"] = pattern(10, 'a')
	vault.objects["B"] = pattern(5, 'b')
	target := filepath.Join(t.TempDir(), "file.bin")
	node := writeChangedTarget(t, target, []byte("previous version, longer than the new one"), []*cache.ChunkInfo{
		{Start: 0, Length: 10, Etag: "A"},
		{Start: 10, Length: 5, Etag: "B"},
	})

	c := newTestClient(t)
	if err := c.restoreFile(context.Background(), target, node, vault, nil, nil); err != nil {
		t.Fatalf("restoreFile: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if want := append(pattern(10, 'a'), pattern(5, 'b')...); !bytes.Equal(got, want) {
		t.Fatalf("content = %q, want %q", got, want)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Windows only has a read-only attribute: os.Chmod maps the owner write bit to it
		// and reports every writable file as 0666.
		if gotW, wantW := fi.Mode().Perm()&0200 != 0, node.Mode.Perm()&0200 != 0; gotW != wantW {
			t.Errorf("writable = %v, want %v (mode %v)", gotW, wantW, fi.Mode().Perm())
		}
	} else if fi.Mode().Perm() != node.Mode.Perm() {
		t.Errorf("mode = %v, want %v", fi.Mode().Perm(), node.Mode.Perm())
	}
	if !fi.ModTime().Equal(node.ModTime) {
		t.Errorf("mtime = %v, want %v", fi.ModTime(), node.ModTime)
	}
	assertNoTempFiles(t, filepath.Dir(target))
}

// While one goroutine is refreshing (here: stuck in the credential request), others must
// not be blocked on restoreKey's lock: reading the generation is immediate and a goroutine
// waiting for the refresh gives up when its context ends.
func TestClient_refreshVaultCredential_DoesNotHoldLockDuringRequest(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	api := &restoreAPI{credentialGate: func(r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
	}}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	c, err := NewClient(WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	vault := newFakeVault()
	rk := &AuthRestore{RecoveryPointID: "rp", ActionID: "act", CreatedAt: "t0", RestoreSessionKey: "key-0"}

	refresherDone := make(chan error, 1)
	go func() { refresherDone <- c.refreshVaultCredential(context.Background(), vault, rk, 0) }()
	<-entered

	genDone := make(chan uint64, 1)
	go func() { genDone <- rk.generation() }()
	select {
	case g := <-genDone:
		if g != 0 {
			t.Fatalf("generation = %d during refresh, want 0", g)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("generation() blocked while a refresh request was in flight")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.refreshVaultCredential(ctx, vault, rk, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter err = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("waiter returned after %v, want about 100ms", d)
	}

	close(release)
	if err := <-refresherDone; err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if g := rk.generation(); g != 1 {
		t.Fatalf("generation = %d after refresh, want 1", g)
	}
	if got := api.credentialHits.Load(); got != 1 {
		t.Fatalf("credential requests = %d, want 1", got)
	}
}

// Cancelling the restore must also stop the refresh request itself, including the retries
// Client.Do makes on its own, and leave restoreKey ready for another refresh.
func TestClient_refreshVaultCredential_CancelStopsRequest(t *testing.T) {
	api := &restoreAPI{credentialGate: func(r *http.Request) { <-r.Context().Done() }}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)

	c, err := NewClient(WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	vault := newFakeVault()
	rk := &AuthRestore{RecoveryPointID: "rp", ActionID: "act", CreatedAt: "t0", RestoreSessionKey: "key-0"}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.refreshVaultCredential(ctx, vault, rk, 0); err == nil {
		t.Fatal("want error after cancel")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("refresh returned after %v, want shortly after cancel", d)
	}
	rk.mu.Lock()
	defer rk.mu.Unlock()
	if rk.refreshing != nil {
		t.Fatal("refreshing left set after a cancelled refresh")
	}
	if rk.gen != 0 {
		t.Fatalf("gen = %d after failed refresh, want 0", rk.gen)
	}
	if rk.RestoreSessionKey != "key-1" {
		t.Fatalf("session key = %q, want the renewed key-1 kept", rk.RestoreSessionKey)
	}
}

// The restore session key URL must be built once, also when ServerURL carries a path.
func TestClient_GetRestoreSessionKey_ServerURLWithPath(t *testing.T) {
	api := &restoreAPI{prefix: "/api/v1"}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)

	c, err := NewClient(WithServerURL(srv.URL + "/api/v1"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.GetRestoreSessionKey("rp", "act", "t0")
	if err != nil {
		t.Fatalf("GetRestoreSessionKey: %v", err)
	}
	if got.RestoreSessionKey != "key-1" {
		t.Fatalf("key = %q, want key-1", got.RestoreSessionKey)
	}
}

func openFDs(t *testing.T) int {
	t.Helper()
	d, err := os.Open("/dev/fd")
	if err != nil {
		t.Skipf("cannot list open file descriptors: %v", err)
	}
	defer d.Close()
	names, err := d.Readdirnames(-1) // names only: stat-ing each fd fails on some of them
	if err != nil {
		t.Skipf("cannot list open file descriptors: %v", err)
	}
	return len(names)
}

// Every restored file must be closed; leaking one descriptor per file ends a large restore
// with "too many open files".
func TestClient_restoreFile_ClosesRestoredFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /dev/fd")
	}
	vault := newFakeVault()
	vault.objects["A"] = pattern(32, 'a')
	node := cache.Node{Mode: 0644, Content: []*cache.ChunkInfo{{Start: 0, Length: 32, Etag: "A"}}}
	dir := t.TempDir()
	c := newTestClient(t)

	// Warm up once so lazily opened descriptors (log file, ...) are not counted.
	if err := c.restoreFile(context.Background(), filepath.Join(dir, "warmup"), node, vault, nil, nil); err != nil {
		t.Fatal(err)
	}
	before := openFDs(t)
	const files = 50
	for i := 0; i < files; i++ {
		target := filepath.Join(dir, fmt.Sprintf("f%d", i))
		if err := c.restoreFile(context.Background(), target, node, vault, nil, nil); err != nil {
			t.Fatalf("restore %s: %v", target, err)
		}
	}
	if after := openFDs(t); after > before {
		t.Fatalf("open descriptors grew from %d to %d after restoring %d files", before, after, files)
	}
	got, err := os.ReadFile(filepath.Join(dir, "f0"))
	if err != nil || !bytes.Equal(got, vault.objects["A"]) {
		t.Fatalf("restored content wrong: %v", err)
	}
}

// The temporary file name must not grow with the target's name: a file whose name is near
// the 255-byte limit must still restore.
func TestClient_restoreFile_LongFileName(t *testing.T) {
	vault := newFakeVault()
	vault.objects["A"] = pattern(16, 'a')
	node := cache.Node{Mode: 0644, Content: []*cache.ChunkInfo{{Start: 0, Length: 16, Etag: "A"}}}
	dir := t.TempDir()
	target := filepath.Join(dir, strings.Repeat("n", 250))

	c := newTestClient(t)
	if err := c.restoreFile(context.Background(), target, node, vault, nil, nil); err != nil {
		t.Fatalf("restoreFile with a 250-byte name: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, vault.objects["A"]) {
		t.Fatalf("restored content wrong: %v", err)
	}
	assertNoTempFiles(t, dir)
}

// Restoring into an existing directory removes temporary files a killed agent left there,
// but keeps this process's own (a download may be in progress) and ordinary files.
func TestClient_restoreDirectory_RemovesStaleRestoreTemps(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	staleToken := "00000000"
	if staleToken == restoreTempToken {
		staleToken = "11111111"
	}
	stale := write(restoreTempPrefix + staleToken + "-0123456789abcdef")
	own, err := tempRestorePath(filepath.Join(dir, "x"))
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Base(own))
	normal := write("report.pdf")

	c := newTestClient(t)
	if err := c.restoreDirectory(context.Background(), dir, cache.Node{Mode: 0755}, nil); err != nil {
		t.Fatalf("restoreDirectory: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp file from another process not removed (stat err %v)", err)
	}
	if _, err := os.Stat(own); err != nil {
		t.Errorf("this process's temp file was removed: %v", err)
	}
	if _, err := os.Stat(normal); err != nil {
		t.Errorf("ordinary file was removed: %v", err)
	}
}

// Access denied on a vault without API-rotated credentials cannot be fixed by retrying.
func TestClient_GetObjectTo_AccessDeniedNonDefaultVaultReturnsImmediately(t *testing.T) {
	vault := newFakeVault()
	vault.credType = "CUSTOM"
	vault.errFor["chunk"] = &smithy.GenericAPIError{Code: "AccessDenied"}

	c := newTestClient(t)
	start := time.Now()
	_, err := c.GetObjectTo(context.Background(), vault, "chunk", nil, tempFile(t), 0, 10)
	if !isAccessDenied(err) {
		t.Fatalf("err = %v, want access denied", err)
	}
	if got := vault.callCount("chunk"); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
	if vault.refreshes != 0 {
		t.Fatalf("RefreshCredential called %d times, want 0", vault.refreshes)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v, want immediate return", d)
	}
}

// GetObject must stop retrying when ctx is cancelled, like GetObjectTo.
func TestClient_GetObject_CancelInterruptsBackoff(t *testing.T) {
	vault := newFakeVault()
	vault.errFor["k"] = errConnReset // retryable, fails forever

	ctx, cancel := context.WithCancel(context.Background())
	var cancelledAt atomic.Int64
	time.AfterFunc(100*time.Millisecond, func() {
		cancelledAt.Store(time.Now().UnixNano())
		cancel()
	})
	c := newTestClient(t)
	_, err := c.GetObject(ctx, vault, "k", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if lag := time.Since(time.Unix(0, cancelledAt.Load())); lag > 100*time.Millisecond {
		t.Fatalf("returned %v after cancel, want the backoff wait to be interrupted", lag)
	}
}

// Only names created by tempRestorePath count as temporary restore files; a user's file that
// merely starts with the same prefix must still be backed up and never deleted.
func TestIsRestoreTempFile(t *testing.T) {
	own, err := tempRestorePath("/x/target")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		filepath.Base(own): true,
		restoreTempPrefix + "0badc0de-0123456789abcdef":       true,
		restoreTempPrefix + "notes.txt":                       false,
		restoreTempPrefix + "0BADC0DE-0123456789ABCDEF":       false,
		restoreTempPrefix + "0badc0de-0123456789abcde":        false,
		restoreTempPrefix + "0badc0de-0123456789abcdef.bak":   false,
		"x" + restoreTempPrefix + "0badc0de-0123456789abcdef": false,
		restoreTempPrefix[1:] + "0badc0de-0123456789abcdef":   false,
	}
	for name, want := range cases {
		if got := IsRestoreTempFile(name); got != want {
			t.Errorf("IsRestoreTempFile(%q) = %v, want %v", name, got, want)
		}
	}
}

// A user's file that only shares the prefix survives the stale-temp cleanup.
func TestClient_restoreDirectory_KeepsUserFileWithTempPrefix(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, restoreTempPrefix+"notes.txt")
	if err := os.WriteFile(user, []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	c := newTestClient(t)
	if err := c.restoreDirectory(context.Background(), dir, cache.Node{Mode: 0755}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(user); err != nil {
		t.Fatalf("user file removed: %v", err)
	}
}

// Outside the Windows read-only case, a failed rename must leave dst alone. Previously an
// empty directory in the way was removed and replaced by the file.
func TestReplaceFile_DoesNotRemoveDirectoryOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fallback applies on Windows")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := os.Mkdir(dst, 0755); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(src, dst); err == nil {
		t.Fatal("want error renaming a file over a directory")
	}
	if fi, err := os.Stat(dst); err != nil || !fi.IsDir() {
		t.Fatalf("dst directory not preserved: %v", err)
	}
}

// With an expired session the credential endpoint answers 401. GetCredentialStorageVault
// must renew the session and succeed, instead of retrying the 401 for minutes and failing.
func TestClient_GetCredentialStorageVault_RenewsExpiredSession(t *testing.T) {
	api := &restoreAPI{rejectStale: true}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	c, err := NewClient(WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	rk := &AuthRestore{RecoveryPointID: "rp", ActionID: "act", CreatedAt: "t0", RestoreSessionKey: "expired"}

	start := time.Now()
	if _, err := c.GetCredentialStorageVault("vault", "act", rk); err != nil {
		t.Fatalf("GetCredentialStorageVault: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v, want the 401 handled without Do's long retry", d)
	}
	if got := api.sessionHits.Load(); got != 1 {
		t.Fatalf("session renewals = %d, want 1", got)
	}
	if got := rk.session(); got.key != "key-1" || got.createdAt != "t1" {
		t.Fatalf("restoreKey session = %+v, want key-1/t1", got)
	}
}

// httpAPIError is an S3-style error carrying both an API code and an HTTP status, like the
// errors aws-sdk-go-v2 returns (checked against the real SDK in the s3 package tests).
type httpAPIError struct {
	code   string
	status int
}

func (e *httpAPIError) Error() string                 { return fmt.Sprintf("%d %s", e.status, e.code) }
func (e *httpAPIError) ErrorCode() string             { return e.code }
func (e *httpAPIError) ErrorMessage() string          { return e.code }
func (e *httpAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }
func (e *httpAPIError) HTTPStatusCode() int           { return e.status }

// A permanent 4xx (the request itself is wrong) fails at once; 408/429 and S3's 400
// RequestTimeout keep being retried.
func TestClient_GetObjectTo_ClientErrorClassification(t *testing.T) {
	c := newTestClient(t)
	for _, tc := range []struct {
		err       error
		retryable bool
	}{
		{&httpAPIError{"InvalidRange", 416}, false},
		{&httpAPIError{"InvalidArgument", 400}, false},
		{&httpAPIError{"RequestTimeout", 400}, true},
		{&httpAPIError{"Timeout", 408}, true},
		{&httpAPIError{"SlowDown", 429}, true},
		{&httpAPIError{"InternalError", 500}, true},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			vault := newFakeVault()
			vault.errFor["chunk"] = tc.err
			ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
			defer cancel()
			_, err := c.GetObjectTo(ctx, vault, "chunk", nil, tempFile(t), 0, 10)
			if err == nil {
				t.Fatal("want error")
			}
			calls := vault.callCount("chunk")
			if tc.retryable && calls < 2 {
				t.Fatalf("calls = %d, want retries", calls)
			}
			if !tc.retryable && calls != 1 {
				t.Fatalf("calls = %d, want 1 (not retryable)", calls)
			}
		})
	}
}

// S3 reports an expired temporary token as 400 ExpiredToken. That is a credential problem:
// a DEFAULT vault refreshes and succeeds instead of failing as a permanent 4xx.
func TestClient_GetObjectTo_ExpiredTokenRefreshes(t *testing.T) {
	api := &restoreAPI{}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	c, err := NewClient(WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	vault := newFakeVault()
	vault.objects["chunk"] = pattern(10, 'e')
	vault.denyUntilRefresh = true
	vault.deniedErr = &httpAPIError{"ExpiredToken", 400}
	rk := &AuthRestore{RecoveryPointID: "rp", ActionID: "act", CreatedAt: "t0", RestoreSessionKey: "key-0"}

	if _, err := c.GetObjectTo(context.Background(), vault, "chunk", rk, tempFile(t), 0, 10); err != nil {
		t.Fatalf("GetObjectTo: %v", err)
	}
	if vault.refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", vault.refreshes)
	}
}

// If the credential request still answers 401 right after the session was renewed, the
// session is renewed again and the request retried once, even though the refresh path
// allows no backoff retries.
func TestClient_refreshVaultCredential_RetriesAfterRenewingOn401(t *testing.T) {
	api := &restoreAPI{}
	api.reject401Once.Store(true)
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	c, err := NewClient(WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	vault := newFakeVault()
	rk := &AuthRestore{RecoveryPointID: "rp", ActionID: "act", CreatedAt: "t0", RestoreSessionKey: "key-0"}

	if err := c.refreshVaultCredential(context.Background(), vault, rk, 0); err != nil {
		t.Fatalf("refreshVaultCredential: %v", err)
	}
	if got := api.credentialHits.Load(); got != 2 {
		t.Fatalf("credential requests = %d, want 2", got)
	}
	if got := api.sessionHits.Load(); got != 2 {
		t.Fatalf("session renewals = %d, want 2", got)
	}
	if rk.generation() != 1 || vault.refreshes != 1 {
		t.Fatalf("gen = %d, refreshes = %d, want 1/1", rk.generation(), vault.refreshes)
	}
}

// bodyTracker counts response bodies handed out and closed.
type bodyTracker struct {
	opened, closed atomic.Int32
}

type trackedBody struct {
	io.ReadCloser
	t    *bodyTracker
	once sync.Once
}

func (b *trackedBody) Close() error {
	b.once.Do(func() { b.t.closed.Add(1) })
	return b.ReadCloser.Close()
}

func (t *bodyTracker) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err == nil {
		t.opened.Add(1)
		resp.Body = &trackedBody{ReadCloser: resp.Body, t: t}
	}
	return resp, err
}

// Do must close every response it discards while retrying, or a long API outage holds one
// connection per retry.
func TestClient_Do_ClosesDiscardedResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	tr := &bodyTracker{}
	c, err := NewClient(WithServerURL(srv.URL), WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	req, err := c.NewRequest(http.MethodGet, "/anything", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := c.Do(req.WithContext(ctx)); err == nil {
		t.Fatal("want error")
	}
	opened, closed := tr.opened.Load(), tr.closed.Load()
	if opened < 2 {
		t.Fatalf("only %d responses; the test needs at least one retry", opened)
	}
	if closed != opened {
		t.Fatalf("closed %d of %d responses", closed, opened)
	}
}

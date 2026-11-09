package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"
)

// indexVault serves one object (index.json) and counts GetObject calls.
type indexVault struct {
	body  []byte
	calls atomic.Int32
}

func (v *indexVault) GetObject(_ context.Context, key string) ([]byte, error) {
	v.calls.Add(1)
	return v.body, nil
}
func (v *indexVault) HeadObject(context.Context, string) (bool, string, error) { return true, "", nil }
func (v *indexVault) PutObject(context.Context, string, []byte) error          { return nil }
func (v *indexVault) GetObjectTo(context.Context, string, io.WriterAt, int64) (int64, error) {
	return 0, errors.New("not used")
}
func (v *indexVault) RefreshCredential(storage_vault.Credential) error { return nil }
func (v *indexVault) ID() (string, string)                             { return "vault", "act" }
func (v *indexVault) Type() storage_vault.Type                         { return storage_vault.Type{} }

func hashOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

const goodIndex = `{"recovery_point_id":"rp","items":{"/a":{"name":"a","type":"file","path":"/a"}},"total_files":1}`

func TestLoadRestoreIndex(t *testing.T) {
	s := &Server{logger: zap.NewNop()}
	good := []byte(goodIndex)

	cacheFile := func(dir string) string { return filepath.Join(dir, "mc", "rp", "index.json") }
	writeCache := func(t *testing.T, dir string, b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(cacheFile(dir)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cacheFile(dir), b, 0600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("downloads, verifies and caches", func(t *testing.T) {
		dir := t.TempDir()
		v := &indexVault{body: good}
		idx, err := s.loadRestoreIndex(context.Background(), v, dir, "mc", "rp", hashOf(good))
		if err != nil || idx.TotalFiles != 1 {
			t.Fatalf("idx=%+v err=%v", idx, err)
		}
		if got, _ := os.ReadFile(cacheFile(dir)); string(got) != goodIndex {
			t.Fatal("verified index not cached")
		}
	})

	t.Run("intact cache is used without download", func(t *testing.T) {
		dir := t.TempDir()
		writeCache(t, dir, good)
		v := &indexVault{body: good}
		if _, err := s.loadRestoreIndex(context.Background(), v, dir, "mc", "rp", hashOf(good)); err != nil {
			t.Fatal(err)
		}
		if v.calls.Load() != 0 {
			t.Fatal("downloaded although cache was intact")
		}
	})

	t.Run("corrupted cache is replaced", func(t *testing.T) {
		dir := t.TempDir()
		writeCache(t, dir, []byte(`{"truncated`))
		v := &indexVault{body: good}
		idx, err := s.loadRestoreIndex(context.Background(), v, dir, "mc", "rp", hashOf(good))
		if err != nil || idx.TotalFiles != 1 {
			t.Fatalf("idx=%+v err=%v", idx, err)
		}
		if got, _ := os.ReadFile(cacheFile(dir)); string(got) != goodIndex {
			t.Fatal("corrupted cache not replaced")
		}
	})

	t.Run("corrupted download fails without panic or cache", func(t *testing.T) {
		dir := t.TempDir()
		v := &indexVault{body: []byte(`{"tampered":true}`)}
		_, err := s.loadRestoreIndex(context.Background(), v, dir, "mc", "rp", hashOf(good))
		if !errors.Is(err, errIndexCorrupted) {
			t.Fatalf("err = %v, want errIndexCorrupted", err)
		}
		if _, err := os.Stat(cacheFile(dir)); !os.IsNotExist(err) {
			t.Fatal("corrupted index was cached")
		}
	})

	t.Run("hash ok but not JSON is an error", func(t *testing.T) {
		bad := []byte("not json")
		_, err := s.loadRestoreIndex(context.Background(), &indexVault{body: bad}, t.TempDir(), "mc", "rp", hashOf(bad))
		if err == nil {
			t.Fatal("want decode error")
		}
	})
}

// Backup/restore goroutines add and remove action contexts while the broker handler cancels
// them. Run with -race.
func TestActionContextConcurrentAccess(t *testing.T) {
	s := &Server{mapActionContext: map[string]contextStruct{}}
	var cancelled atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		id := string(rune('a' + i))
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				ctx, cancel := context.WithCancel(context.Background())
				s.setActionContext(id, contextStruct{ctx: ctx, cancel: func() { cancelled.Add(1); cancel() }})
				s.removeActionContext(id)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s.cancelActionContext(id)
			}
		}()
	}
	wg.Wait()
	if len(s.mapActionContext) != 0 {
		t.Fatalf("%d entries left", len(s.mapActionContext))
	}
}

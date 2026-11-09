package backupapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/panjf2000/ants/v2"

	"github.com/bizflycloud/bizfly-backup/pkg/cache"
)

func chunk(start, length uint, etag string) *cache.ChunkInfo {
	return &cache.ChunkInfo{Start: start, Length: length, Etag: etag}
}

func TestNormalizeChunks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      []*cache.ChunkInfo
		want    []string // etags in order
		wantErr bool
	}{
		{"empty", nil, nil, false},
		{"contiguous", []*cache.ChunkInfo{chunk(0, 5, "a"), chunk(5, 3, "b")}, []string{"a", "b"}, false},
		{"unsorted", []*cache.ChunkInfo{chunk(5, 3, "b"), chunk(0, 5, "a")}, []string{"a", "b"}, false},
		{"duplicates from a retried backup", []*cache.ChunkInfo{
			chunk(0, 5, "a"), chunk(5, 3, "b"), chunk(0, 5, "a"), chunk(5, 3, "b"), chunk(8, 2, "c"),
		}, []string{"a", "b", "c"}, false},
		{"gap", []*cache.ChunkInfo{chunk(0, 5, "a"), chunk(6, 3, "b")}, nil, true},
		{"not from zero", []*cache.ChunkInfo{chunk(2, 5, "a")}, nil, true},
		{"different chunk at same offset", []*cache.ChunkInfo{chunk(0, 5, "a"), chunk(0, 5, "x")}, nil, true},
		{"overlap", []*cache.ChunkInfo{chunk(0, 5, "a"), chunk(3, 5, "b")}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeChunks(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidChunkList) {
					t.Fatalf("err = %v, want ErrInvalidChunkList", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			var etags []string
			for _, c := range got {
				etags = append(etags, c.Etag)
			}
			if len(etags) != len(tc.want) {
				t.Fatalf("chunks = %v, want %v", etags, tc.want)
			}
			for i := range etags {
				if etags[i] != tc.want[i] {
					t.Fatalf("chunks = %v, want %v", etags, tc.want)
				}
			}
		})
	}
}

func sum(b []byte) cache.Sha256Hash {
	h := sha256.Sum256(b)
	return h[:]
}

// Content whose sha256 differs from the backup must not replace the existing file.
func TestClient_restoreFile_HashMismatchKeepsTarget(t *testing.T) {
	vault := newFakeVault()
	vault.objects["A"] = pattern(10, 'a')
	target := filepath.Join(t.TempDir(), "file.bin")
	old := []byte("previous version")
	node := writeChangedTarget(t, target, old, []*cache.ChunkInfo{chunk(0, 10, "A")})
	node.Sha256Hash = sum([]byte("something else entirely"))

	c := newTestClient(t)
	err := c.restoreFile(context.Background(), target, node, vault, nil, nil)
	if !errors.Is(err, ErrRestoredHashMismatch) {
		t.Fatalf("err = %v, want ErrRestoredHashMismatch", err)
	}
	if got, _ := os.ReadFile(target); string(got) != string(old) {
		t.Fatalf("target replaced by unverified content: %q", got)
	}
	assertNoTempFiles(t, filepath.Dir(target))
}

// A correct hash passes, and identical duplicate chunks (left by an older agent's backup
// retry) are downloaded once each.
func TestClient_restoreFile_VerifiedWithDuplicateChunks(t *testing.T) {
	vault := newFakeVault()
	vault.objects["A"] = pattern(10, 'a')
	vault.objects["B"] = pattern(6, 'b')
	content := append(pattern(10, 'a'), pattern(6, 'b')...)
	node := cache.Node{
		Mode:       0644,
		Sha256Hash: sum(content),
		Content:    []*cache.ChunkInfo{chunk(0, 10, "A"), chunk(10, 6, "B"), chunk(0, 10, "A"), chunk(10, 6, "B")},
	}
	target := filepath.Join(t.TempDir(), "file.bin")

	c := newTestClient(t)
	if err := c.restoreFile(context.Background(), target, node, vault, nil, nil); err != nil {
		t.Fatalf("restoreFile: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != string(content) {
		t.Fatalf("content = %q, want %q", got, content)
	}
	if vault.callCount("A") != 1 || vault.callCount("B") != 1 {
		t.Fatalf("calls A=%d B=%d, want 1 each", vault.callCount("A"), vault.callCount("B"))
	}
}

// An index whose chunks leave a gap is rejected before anything is downloaded or written.
func TestClient_restoreFile_GapInChunksFailsEarly(t *testing.T) {
	vault := newFakeVault()
	vault.objects["A"] = pattern(10, 'a')
	vault.objects["B"] = pattern(10, 'b')
	node := cache.Node{Mode: 0644, Content: []*cache.ChunkInfo{chunk(0, 10, "A"), chunk(20, 10, "B")}}
	target := filepath.Join(t.TempDir(), "file.bin")

	c := newTestClient(t)
	err := c.restoreFile(context.Background(), target, node, vault, nil, nil)
	if !errors.Is(err, ErrInvalidChunkList) {
		t.Fatalf("err = %v, want ErrInvalidChunkList", err)
	}
	if vault.callCount("A")+vault.callCount("B") != 0 {
		t.Fatal("chunks downloaded for an invalid index entry")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target created: %v", err)
	}
	assertNoTempFiles(t, filepath.Dir(target))
}

// A cancelled restore must report cancellation, not success, and stop scheduling items.
func TestClient_RestoreDirectory_CancelledIsNotSuccess(t *testing.T) {
	vault := newFakeVault()
	dir := t.TempDir()
	index := cache.Index{Items: map[string]*cache.Node{}}
	for _, name := range []string{"a", "b", "c"} {
		vault.objects[name] = pattern(4, name[0])
		index.Items[name] = &cache.Node{
			Type: "file", Name: name, Mode: 0644,
			AbsolutePath: filepath.Join(dir, "src", name), BasePath: filepath.Join(dir, "src"), RelativePath: name,
			Content: []*cache.ChunkInfo{chunk(0, 4, name)},
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := newTestClient(t)
	err := c.RestoreDirectory(ctx, index, filepath.Join(dir, "dst"), vault, nil, nil)
	if !errors.Is(err, ErrorGotCancelRequest) {
		t.Fatalf("err = %v, want ErrorGotCancelRequest", err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if n := vault.callCount(name); n != 0 {
			t.Errorf("item %s downloaded %d times after cancel", name, n)
		}
	}
}

// A file deleted between the scan and the backup is recorded as empty, without stale chunks
// and without panicking (the hash used to be computed from a nil hash.Hash).
func TestClient_ChunkFileToBackup_VanishedFile(t *testing.T) {
	pool, err := ants.NewPool(1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release()
	item := &cache.Node{
		AbsolutePath: filepath.Join(t.TempDir(), "gone.log"),
		Content:      []*cache.ChunkInfo{chunk(0, 5, "stale")},
		Sha256Hash:   sum([]byte("stale")),
	}
	c := newTestClient(t)
	if _, err := c.ChunkFileToBackup(context.Background(), pool, item, nil, nil, nil, nil, "rp", "bd"); err != nil {
		t.Fatalf("ChunkFileToBackup: %v", err)
	}
	if item.Content != nil || item.Sha256Hash != nil {
		t.Fatalf("vanished file kept content=%v hash=%x", item.Content, []byte(item.Sha256Hash))
	}
}

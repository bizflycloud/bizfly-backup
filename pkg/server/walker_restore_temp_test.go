package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/bizflycloud/bizfly-backup/pkg/cache"
	"github.com/bizflycloud/bizfly-backup/pkg/progress"
)

// Temporary files left by an interrupted restore must not be backed up.
func TestWalkerDir_SkipsRestoreTempFiles(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "data.txt")
	// Shares the prefix but not the full temp-file pattern: it is user data.
	userPrefixed := filepath.Join(dir, ".bizfly-restore-notes.txt")
	temp := filepath.Join(dir, ".bizfly-restore-0badc0de-0123456789abcdef")
	for _, p := range []string{keep, userPrefixed, temp} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	index := cache.NewIndex("bd", "rp")
	_, total, err := WalkerDir(dir, index, progress.NewProgress(time.Second), zap.NewNop())
	if err != nil {
		t.Fatalf("WalkerDir: %v", err)
	}
	if _, ok := index.Items[temp]; ok {
		t.Errorf("restore temp file was indexed")
	}
	if _, ok := index.Items[keep]; !ok {
		t.Errorf("ordinary file was not indexed")
	}
	if _, ok := index.Items[userPrefixed]; !ok {
		t.Errorf("user file starting with the temp prefix was not indexed")
	}
	if total != 2 {
		t.Errorf("TotalFiles = %d, want 2", total)
	}
}

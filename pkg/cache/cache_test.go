package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveIndexAndSaveChunk_StreamingEncoder(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRepository(dir, "mc", "rp")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}

	idx := &Index{Items: map[string]*Node{}}
	if err := r.SaveIndex(idx); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	buf, err := os.ReadFile(filepath.Join(dir, "mc", "rp", "index.json"))
	if err != nil {
		t.Fatalf("read index.json: %v", err)
	}
	var got Index
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("unmarshal index: %v (raw=%q)", err, string(buf))
	}

	ch := NewChunk("bd", "rp")
	ch.Chunks["abc"] = []string{"1", "100"}
	if err := r.SaveChunk(ch); err != nil {
		t.Fatalf("SaveChunk: %v", err)
	}
	buf, err = os.ReadFile(filepath.Join(dir, "mc", "rp", "chunk.json"))
	if err != nil {
		t.Fatalf("read chunk.json: %v", err)
	}
	var gotC Chunk
	if err := json.Unmarshal(buf, &gotC); err != nil {
		t.Fatalf("unmarshal chunk: %v", err)
	}
	if v, ok := gotC.Chunks["abc"]; !ok || len(v) != 2 || v[0] != "1" || v[1] != "100" {
		t.Fatalf("chunk round-trip mismatch: got=%v", gotC.Chunks)
	}

	tempDir := filepath.Join(dir, "mc", "rp", "tmp")
	entries, _ := os.ReadDir(tempDir)
	for _, e := range entries {
		t.Errorf("leftover temp file: %s", e.Name())
	}
}

package backupapi

import (
	"sync"
	"testing"
)

func TestChunkBufPool_SizingAndReuse(t *testing.T) {
	b1 := getChunkBuf(1024)
	if len(b1) != 1024 {
		t.Fatalf("want len 1024, got %d", len(b1))
	}
	for i := range b1 {
		b1[i] = byte(i)
	}
	putChunkBuf(b1)

	b2 := getChunkBuf(2048)
	if len(b2) != 2048 {
		t.Fatalf("want len 2048, got %d", len(b2))
	}
	putChunkBuf(b2)

	b3 := getChunkBuf(10 * 1024 * 1024)
	if len(b3) != 10*1024*1024 {
		t.Fatalf("want len 10MB, got %d", len(b3))
	}
	putChunkBuf(b3)
}

func TestChunkBufPool_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b := getChunkBuf(1024 + n)
				copy(b, []byte("hello"))
				putChunkBuf(b)
			}
		}(i)
	}
	wg.Wait()
}

func TestMd5Pool_Reuse(t *testing.T) {
	h := md5Pool.Get().(interface {
		Reset()
		Write([]byte) (int, error)
		Sum([]byte) []byte
	})
	h.Reset()
	_, _ = h.Write([]byte("abc"))
	s1 := h.Sum(nil)

	h.Reset()
	_, _ = h.Write([]byte("abc"))
	s2 := h.Sum(nil)

	if string(s1) != string(s2) {
		t.Fatalf("md5 hasher reuse inconsistent: %x vs %x", s1, s2)
	}
}

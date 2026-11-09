package s3

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"
)

// newTestS3 returns an S3 vault whose client talks to srv. backupClient is left nil: any
// attempt to refresh credentials from inside the s3 package would panic.
func newTestS3(t *testing.T, srv *httptest.Server) *S3 {
	t.Helper()
	s := &S3{
		Id:             "vault",
		ActionID:       "act",
		StorageBucket:  "bucket",
		CredentialType: "DEFAULT",
		Location:       srv.URL,
		Region:         "us-east-1",
		logger:         zap.NewNop(),
	}
	c, err := newS3Client(srv.URL, "us-east-1", "ak", "sk", "", srv.Client(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	s.setClient(c)
	return s
}

func tempDst(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "dst"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// Access denied must be returned to the caller, which refreshes once for all goroutines.
// Refreshing here as well would bypass that and swap the client once per goroutine.
func TestS3_GetObjectTo_AccessDeniedReturnedWithoutRefresh(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>expired</Message></Error>`))
	}))
	t.Cleanup(srv.Close)

	s := newTestS3(t, srv)
	_, err := s.GetObjectTo(context.Background(), "key", tempDst(t), 0)
	if code := apiErrCode(err); code != "AccessDenied" {
		t.Fatalf("err = %v (code %q), want AccessDenied", err, code)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

// RefreshCredential swaps the client while downloads are using it. Run with -race: the
// client pointer must be read and replaced atomically.
func TestS3_RefreshCredentialWhileDownloading(t *testing.T) {
	body := []byte("chunk-content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	s := newTestS3(t, srv)

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dst := tempDst(t)
			for i := 0; i < 20; i++ {
				n, err := s.GetObjectTo(context.Background(), "key", dst, 0)
				if err != nil || n != int64(len(body)) {
					t.Errorf("GetObjectTo n=%d err=%v", n, err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := s.RefreshCredential(storage_vault.Credential{AwsAccessKeyId: "ak2", AwsSecretAccessKey: "sk2"}); err != nil {
				t.Errorf("RefreshCredential: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// backupapi classifies download errors by HTTP status (4xx other than 403/408/429 is not
// retried). That relies on the SDK's error chain exposing the status: check it does.
func TestS3_GetObjectTo_ErrorExposesHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidRange</Code><Message>bad</Message></Error>`))
	}))
	t.Cleanup(srv.Close)

	_, err := newTestS3(t, srv).GetObjectTo(context.Background(), "key", tempDst(t), 0)
	if code := apiErrCode(err); code != "InvalidRange" {
		t.Fatalf("code = %q (err %v), want InvalidRange", code, err)
	}
	var se interface{ HTTPStatusCode() int }
	if !errors.As(err, &se) || se.HTTPStatusCode() != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("error does not expose HTTP status 416: %v", err)
	}
}

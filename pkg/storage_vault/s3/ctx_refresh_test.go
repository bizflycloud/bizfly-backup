package s3

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"
)

func failingS3Server(t *testing.T, status int, code string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + code + `</Code><Message>x</Message></Error>`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// Cancelling must stop GetObject (index.json download) and PutObject (chunk upload) while
// they wait between retries, instead of after up to 3 minutes. PutObject's HEAD check
// (VerifyObject) used to retry without waiting at all.
func TestS3_CancelStopsRetries(t *testing.T) {
	srv, _ := failingS3Server(t, http.StatusInternalServerError, "InternalError")
	s := newTestS3(t, srv)
	s.CredentialType = "OTHER"

	for name, call := range map[string]func(ctx context.Context) error{
		"GetObject": func(ctx context.Context) error { _, err := s.GetObject(ctx, "index.json"); return err },
		"PutObject": func(ctx context.Context) error { return s.PutObject(ctx, "chunk", []byte("data")) },
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := call(ctx)
			if err == nil {
				t.Fatal("want error")
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("returned after %v, want shortly after the 300ms cancel (err %v)", d, err)
			}
		})
	}
}

// A credential refused again right after a successful refresh used to make GetObject loop
// refresh → retry with no wait for as long as the API kept answering. It now refreshes once.
func TestS3_GetObject_RefreshesOnlyOnce(t *testing.T) {
	srv, _ := failingS3Server(t, http.StatusForbidden, "AccessDenied")
	var credHits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/credential") {
			http.NotFound(w, r)
			return
		}
		credHits.Add(1)
		_ = json.NewEncoder(w).Encode(backupapi.StorageVault{
			Credential: storage_vault.Credential{AwsAccessKeyId: "ak", AwsSecretAccessKey: "sk"},
		})
	}))
	t.Cleanup(api.Close)
	bc, err := backupapi.NewClient(backupapi.WithServerURL(api.URL))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestS3(t, srv)
	s.backupClient = bc

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = s.GetObject(ctx, "index.json")
	if code := apiErrCode(err); code != "AccessDenied" {
		t.Fatalf("err = %v, want AccessDenied", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("GetObject only stopped because of the test timeout")
	}
	if got := credHits.Load(); got != 1 {
		t.Fatalf("credential refreshed %d times, want 1", got)
	}
}

// Replacing the client on refresh closes the old client's idle connections instead of
// leaving them open until they time out.
func TestS3_RefreshCredentialClosesOldIdleConnections(t *testing.T) {
	var closed atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateClosed {
			closed.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	s := newTestS3(t, srv)
	cred := storage_vault.Credential{AwsAccessKeyId: "ak", AwsSecretAccessKey: "sk"}
	if err := s.RefreshCredential(cred); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetObjectTo(context.Background(), "k", tempDst(t), 0); err != nil {
		t.Fatal(err)
	}
	// The connection used above is now idle in the first refreshed client.
	if err := s.RefreshCredential(cred); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if closed.Load() == 0 {
		t.Fatal("old client's idle connection was not closed")
	}
}

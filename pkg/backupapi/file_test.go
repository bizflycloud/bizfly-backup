package backupapi

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_uploadFile(t *testing.T) {
	setUp()
	defer tearDown()

	fn := "test-upload-file-1"
	content := "foo\n"
	buf := strings.NewReader(content)

	mux.HandleFunc("/api/v1"+client.uploadFilePath(fn), func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NotEmpty(t, r.Header.Get("User-Agent"))
		require.NotEmpty(t, r.Header.Get("Date"))
		require.NotEmpty(t, r.Header.Get("Authorization"))
		require.True(t, strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; "))
		require.NoError(t, r.ParseMultipartForm(64))
		file, handler, err := r.FormFile("data")
		require.NoError(t, err)
		defer file.Close()
		assert.Equal(t, fn, handler.Filename)
		var data []byte
		buf := bytes.NewBuffer(data)
		_, err = io.Copy(buf, file)
		assert.NoError(t, err)
		assert.Equal(t, content, buf.String())
	})
	fi, _ := ioutil.TempFile("", "bizfly-backup-agent-backup-*")
	stats, _ := fi.Stat()
	pw := NewProgressWriter(ioutil.Discard)
	assert.NoError(t, client.UploadFile(fn, buf, pw, stats, fn, false))
}

func TestClient_uploadMultipart(t *testing.T) {
	setUp()
	defer tearDown()

	fn := "test-upload-file-2"
	content := strings.Repeat("a", 20*1000*1000) + "\n"
	buf := strings.NewReader(content)

	mux.HandleFunc("/api/v1"+client.initMultipartPath(fn), func(w http.ResponseWriter, r *http.Request) {
		m := Multipart{
			UploadID: "foo",
			FileName: "bar",
		}
		_ = json.NewEncoder(w).Encode(&m)
	})

	expectedNum := 0
	var mu sync.Mutex
	mux.HandleFunc("/api/v1"+client.uploadPartPath(fn), func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		expectedNum++
		mu.Unlock()

		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "foo", r.URL.Query().Get("upload_id"))
		partNumStr := r.URL.Query().Get("part_number")
		partNum, _ := strconv.ParseInt(partNumStr, 10, 64)
		assert.Greater(t, partNum, int64(0))
	})

	mux.HandleFunc("/api/v1"+client.completeMultipartPath(fn), func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "foo", r.URL.Query().Get("upload_id"))
		assert.Equal(t, 2, expectedNum)
	})
	fi, _ := ioutil.TempFile("", "bizfly-backup-agent-backup-*")
	stats, _ := fi.Stat()
	pw := NewProgressWriter(ioutil.Discard)
	assert.NoError(t, client.UploadFile(fn, buf, pw, stats, fn, true))

}

func TestConvertPermission(t *testing.T) {
	perm := "-rwxrwxr-x"
	assert.Equal(t, uint64(0x1fd), ConvertPermission(perm))
}

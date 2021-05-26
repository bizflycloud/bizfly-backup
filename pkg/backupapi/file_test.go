package backupapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClient_saveFileInfoPath(t *testing.T) {
	setUp()
	defer tearDown()

	recoveryPointID := "recovery-point-id"
	saveFile := client.saveFileInfoPath(recoveryPointID)
	assert.Equal(t, "/agent/recovery-points/recovery-point-id/file", saveFile)
}

// func TestClient_UploadFile(t *testing.T) {
// 	setUp()
// 	defer tearDown()

// 	recoveryPointID := "recovery-point-id"
// 	backupDir := "backup-dir"
// 	fileID := "file-id"
// 	content := "foo\n"
// 	buf := strings.NewReader(content)

// 	mux.HandleFunc("/api/v1"+client.saveChunk(recoveryPointID, fileID), func(w http.ResponseWriter, r *http.Request) {
// 		require.Equal(t, http.MethodPost, r.Method)
// 		require.NotEmpty(t, r.Header.Get("User-Agent"))
// 		require.NotEmpty(t, r.Header.Get("Date"))
// 		require.NotEmpty(t, r.Header.Get("Authorization"))
// 		require.True(t, strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; "))
// 		require.NoError(t, r.ParseMultipartForm(64))
// 		file, handler, err := r.FormFile("data")
// 		require.NoError(t, err)
// 		defer file.Close()
// 		assert.Equal(t, fn, handler.Filename)
// 		var data []byte
// 		buf := bytes.NewBuffer(data)
// 		_, err = io.Copy(buf, file)
// 		assert.NoError(t, err)
// 		assert.Equal(t, content, buf.String())
// 	})

// 	pw := NewProgressWriter(ioutil.Discard)
// 	assert.NoError(t, client.UploadFile(fn, buf, pw, false))
// }

// func TestClient_uploadMultipart(t *testing.T) {
// 	setUp()
// 	defer tearDown()

// 	fn := "test-upload-file-2"
// 	content := strings.Repeat("a", 20*1000*1000) + "\n"
// 	buf := strings.NewReader(content)

// 	mux.HandleFunc("/api/v1"+client.initMultipartPath(fn), func(w http.ResponseWriter, r *http.Request) {
// 		m := Multipart{
// 			UploadID: "foo",
// 			FileName: "bar",
// 		}
// 		_ = json.NewEncoder(w).Encode(&m)
// 	})

// 	expectedNum := 0
// 	var mu sync.Mutex
// 	mux.HandleFunc("/api/v1"+client.uploadPartPath(fn), func(w http.ResponseWriter, r *http.Request) {
// 		mu.Lock()
// 		expectedNum++
// 		mu.Unlock()

// 		assert.Equal(t, http.MethodPut, r.Method)
// 		assert.Equal(t, "foo", r.URL.Query().Get("upload_id"))
// 		partNumStr := r.URL.Query().Get("part_number")
// 		partNum, _ := strconv.ParseInt(partNumStr, 10, 64)
// 		assert.Greater(t, partNum, int64(0))
// 	})

// 	mux.HandleFunc("/api/v1"+client.completeMultipartPath(fn), func(w http.ResponseWriter, r *http.Request) {
// 		assert.Equal(t, http.MethodPost, r.Method)
// 		assert.Equal(t, "foo", r.URL.Query().Get("upload_id"))
// 		assert.Equal(t, 2, expectedNum)
// 	})

// 	pw := NewProgressWriter(ioutil.Discard)
// 	assert.NoError(t, client.UploadFile(fn, buf, pw, true))

// }

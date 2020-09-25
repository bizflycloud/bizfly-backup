package backupapi

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_uploadFile(t *testing.T) {
	setUp()
	defer tearDown()

	RecoveryPointId := "1"
	fn := "test-upload-file"
	content := "foo\n"
	buf := strings.NewReader(content)

	mux.HandleFunc(client.uploadFilePath(RecoveryPointId), func(w http.ResponseWriter, r *http.Request) {
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

	pw := NewProgressWriter(ioutil.Discard)
	assert.NoError(t, client.UploadFile(fn, buf, pw))
}

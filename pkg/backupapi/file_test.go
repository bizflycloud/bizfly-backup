package backupapi

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_uploadFile(t *testing.T) {
	setUp()
	defer tearDown()

	fn := "test-upload-file"
	content := "foo\n"
	buf := strings.NewReader(content)

	mux.HandleFunc(uploadFilePath, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

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

	assert.NoError(t, client.UploadFile(fn, buf))
}

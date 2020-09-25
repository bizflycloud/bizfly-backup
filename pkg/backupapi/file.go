package backupapi

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"

	"github.com/hashicorp/go-retryablehttp"
)

// File ...
type File struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Size        int    `json:"size"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	ContentType string `json:"content_type"`
	Etag        string `json:"eTag"`
}

// Multipart ...
type Multipart struct {
	UploadID string `json:"upload_id"`
	FileName string `json:"file_name"`
	Parts    []byte `json:"parts"`
}

// Part ...
type Part struct {
	PartNumber int    `json:"part_number"`
	Size       int    `json:"size"`
	Etag       string `json:"etag"`
}

func (c *Client) uploadFilePath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/file", recoveryPointID)
}

// UploadFile uploads given file to server.
func (c *Client) UploadFile(fn string, r io.Reader, pw io.Writer) error {
	bodyBuf := &bytes.Buffer{}
	bodyWriter := multipart.NewWriter(bodyBuf)
	fileWriter, err := bodyWriter.CreateFormFile("data", fn)
	if err != nil {
		return fmt.Errorf("bodyWriter.CreateFormFile: %w", err)
	}

	_, err = io.Copy(fileWriter, r)
	if err != nil {
		return err
	}

	contentType := bodyWriter.FormDataContentType()
	if err := bodyWriter.Close(); err != nil {
		return err
	}

	relPath := c.uploadFilePath(fn)
	if c.ServerURL.Path != "" && c.ServerURL.Path != "/" {
		relPath = path.Join(c.ServerURL.Path, relPath)
	}
	relURL, err := url.Parse(relPath)
	if err != nil {
		return err
	}

	u := c.ServerURL.ResolveReference(relURL)

	req, err := http.NewRequest(http.MethodPost, u.String(), io.TeeReader(bodyBuf, pw))
	if err != nil {
		return err
	}

	retryClient := retryablehttp.NewClient()
	retryClient.RetryMax = 50 // Should configurable this?
	resp, err := c.do(retryClient.StandardClient(), req, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.Copy(ioutil.Discard, resp.Body)
	return err
}

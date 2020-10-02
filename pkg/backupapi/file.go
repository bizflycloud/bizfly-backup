package backupapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"sync"

	"github.com/hashicorp/go-retryablehttp"
)

const MultipartUploadLowerBound = 15 * 1000 * 1000

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

func (c *Client) urlStringFromRelPath(relPath string) (string, error) {
	if c.ServerURL.Path != "" && c.ServerURL.Path != "/" {
		relPath = path.Join(c.ServerURL.Path, relPath)
	}
	relURL, err := url.Parse(relPath)
	if err != nil {
		return "", err
	}

	u := c.ServerURL.ResolveReference(relURL)
	return u.String(), nil
}

func (c *Client) uploadFile(fn string, r io.Reader, pw io.Writer) error {
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

	reqURL, err := c.urlStringFromRelPath(c.uploadFilePath(fn))
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, reqURL, io.TeeReader(bodyBuf, pw))
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

func (c *Client) uploadMultipart(recoveryPointID string, r io.Reader, pw io.Writer) error {
	ctx := context.Background()
	m, err := c.InitMultipart(ctx, recoveryPointID)
	if err != nil {
		return err
	}

	partNum := 0
	var wg sync.WaitGroup
	var errs []error
	var mu sync.Mutex
	for {
		partNum++
		bodyBuf := &bytes.Buffer{}
		bodyWriter := multipart.NewWriter(bodyBuf)
		fileWriter, err := bodyWriter.CreateFormFile("data", recoveryPointID+"-"+strconv.Itoa(partNum))
		if err != nil {
			return fmt.Errorf("bodyWriter.CreateFormFile: %w", err)
		}
		written, err := io.CopyN(fileWriter, r, MultipartUploadLowerBound)
		if err != nil && err != io.EOF {
			return err
		}
		if written == 0 && err == io.EOF {
			break
		}

		wg.Add(1)
		go func(bodyWriter *multipart.Writer, partNum int) {
			defer wg.Done()
			contentType := bodyWriter.FormDataContentType()
			if err := bodyWriter.Close(); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}

			reqURL, err := c.urlStringFromRelPath(c.uploadPartPath(recoveryPointID))
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			req, err := http.NewRequest(http.MethodPut, reqURL, io.TeeReader(bodyBuf, pw))
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			q := req.URL.Query()
			q.Add("part_number", strconv.Itoa(partNum))
			q.Add("upload_id", m.UploadID)
			req.URL.RawQuery = q.Encode()

			retryClient := retryablehttp.NewClient()
			retryClient.RetryMax = 50 // Should configurable this?
			resp, err := c.do(retryClient.StandardClient(), req, contentType)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			defer resp.Body.Close()

			if _, err := io.Copy(ioutil.Discard, resp.Body); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(bodyWriter, partNum)
	}
	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("upload multiparts fails: %v", errs)
	}

	return c.CompleteMultipart(ctx, recoveryPointID, m.UploadID)
}

// UploadFile uploads given file to server.
func (c *Client) UploadFile(fn string, r io.Reader, pw io.Writer, batch bool) error {
	if batch {
		return c.uploadMultipart(fn, r, pw)
	}
	return c.uploadFile(fn, r, pw)
}

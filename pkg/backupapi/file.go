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
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

const (
	MultipartUploadLowerBound = 15 * 1000 * 1000
	MaximumParts = 10000
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

func setFields(fi os.FileInfo, w *multipart.Writer) error {
	w.WriteField("size", strconv.FormatInt(fi.Size(), 10))
	w.WriteField("is_dir", strconv.FormatBool(fi.IsDir()))
	w.WriteField("mode", fi.Mode().String())
	w.WriteField("modified_at", fi.ModTime().Format(time.RFC3339))
	return nil
}

func (c *Client) uploadFile(fn string, r io.Reader, pw io.Writer, fi os.FileInfo, path string) error {
	bodyBuf := &bytes.Buffer{}
	bodyWriter := multipart.NewWriter(bodyBuf)
	setFields(fi, bodyWriter)
	bodyWriter.WriteField("path", path)
	bodyWriter.WriteField("name", path)
	// TODO add hash of file
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

func (c *Client) uploadMultipart(recoveryPointID string, r io.Reader, pw io.Writer, info os.FileInfo, path string) error {
	ctx := context.Background()
	m, err := c.InitMultipart(ctx, recoveryPointID, &InitMultiPartUploadRequest{Name: path})
	partSize := int64(MultipartUploadLowerBound)
	partNums := info.Size()/MultipartUploadLowerBound
	if partNums > MaximumParts {
		partSize = (partNums / MaximumParts + 1) * MultipartUploadLowerBound
	}
	if err != nil {
		return err
	}

	bufCh := make(chan []byte, 30)
	go func() {
		defer close(bufCh)
		b := make([]byte, partSize)
		for {
			n, err := r.Read(b)
			if err != nil {
				return
			}
			bufCh <- b[:n]
		}
	}()

	partNum := 0
	var wg sync.WaitGroup
	var errs []error
	var mu sync.Mutex
	sem := make(chan struct{}, 45)
	rc := retryablehttp.NewClient()
	rc.RetryMax = 50 // TODO: configurable?
	rcStd := rc.StandardClient()
	for buf := range bufCh {
		sem <- struct{}{}
		buf := buf
		partNum++
		wg.Add(1)
		go func(buf []byte, partNum int) {
			defer func() {
				<-sem
				wg.Done()
			}()
			b := new(bytes.Buffer)
			bodyWriter := multipart.NewWriter(b)
			fileWriter, err := bodyWriter.CreateFormFile("data", recoveryPointID+"-"+strconv.Itoa(partNum))
			if err != nil {
				return
			}
			_, _ = fileWriter.Write(buf)
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
			req, err := http.NewRequest(http.MethodPut, reqURL, io.TeeReader(b, pw))
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			q := req.URL.Query()
			q.Add("part_number", strconv.Itoa(partNum))
			q.Add("upload_id", m.UploadID)
			q.Add("name", path)
			req.URL.RawQuery = q.Encode()

			resp, err := c.do(rcStd, req, contentType)
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
		}(buf, partNum)
	}
	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("upload multiparts fails: %v", errs)
	}
	rc.HTTPClient.CloseIdleConnections()

	cmpur := &CompleteMultiPartUploadRequest{
		Path:       path,
		Size:       int(info.Size()),
		Mode:       info.Mode().String(),
		IsDir:      info.IsDir(),
		ModifiedAt: info.ModTime().Format(time.RFC3339),
		Name:       path,
	}
	fmt.Errorf("%v", cmpur)
	return c.CompleteMultipart(ctx, recoveryPointID, m.UploadID, cmpur)
}

// UploadFile uploads given file to server.
func (c *Client) UploadFile(fn string, r io.Reader, pw io.Writer, fi os.FileInfo, path string, batch bool) error {
	if batch {
		return c.uploadMultipart(fn, r, pw, fi, path)

	}
	return c.uploadFile(fn, r, pw, fi, path)
}

// DownloadFile
func (c *Client) DownloadItems(items []ItemsResponse, createdAt string, restoreSessionKey string, recoveryPointID string, dir string) error {
	var wg sync.WaitGroup
	var errs []error
	var mu sync.Mutex
	rc := retryablehttp.NewClient()
	rc.RetryMax = 50 // TODO: configurable?
	rcStd := rc.StandardClient()
	for _, item := range items {
		if strings.EqualFold(item.ItemType, ItemFileType) {
			wg.Add(1)
			go func(item ItemsResponse) {
				defer wg.Done()
				fi, err := os.OpenFile(fmt.Sprintf("%s/%s", dir, item.ItemName), os.O_RDWR|os.O_CREATE|os.O_TRUNC, os.FileMode(ConvertPermission(item.Mode)))
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
				defer fi.Close()

				reqURL, err := c.urlStringFromRelPath(c.downloadFileContentPath(recoveryPointID))
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}

				req, err := http.NewRequest(http.MethodGet, reqURL, nil)
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
				req.Header.Add("X-Session-Created-At", createdAt)
				req.Header.Add("X-Restore-Session-Key", restoreSessionKey)
				// walk files in
				q := req.URL.Query()
				q.Set("name", strings.Split(item.ItemName, recoveryPointID)[1])
				req.URL.RawQuery = q.Encode()

				resp, err := c.do(rcStd, req, "application/json")
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
				if err := checkResponse(resp); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
				defer resp.Body.Close()

				_, err = io.Copy(fi, resp.Body)

			}(item)
		}
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("Download recovery point fails: %v", errs)
	}
	return nil
}

func perm(perm string) int {
	var permInt int
	for _, p := range strings.Split(perm, "") {
		switch p {
		case "r":
			permInt += 4
		case "w":
			permInt += 2
		case "x":
			permInt += 1
		}
	}
	return permInt
}

func ConvertPermission(permStr string) uint64 {
	if len(permStr) < 10 {
		return 420
	}
	output, err := strconv.ParseUint(fmt.Sprintf("%d%d%d", perm(permStr[1:4]), perm(permStr[4:7]), perm(permStr[7:10])), 8, 64)
	if err != nil {
		return 420
	}
	return output

}

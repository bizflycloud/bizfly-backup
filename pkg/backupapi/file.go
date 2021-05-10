package backupapi

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/bizflycloud/bizfly-backup/pkg/volume"
	"github.com/restic/chunker"
	"io"
	"io/fs"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/hashicorp/go-retryablehttp"
)

const MultipartUploadLowerBound = 15 * 1000 * 1000

// FileInfoRequest is metadata of the file to backup.
type FileInfo struct {
	ItemName     string `json:"item_name"`
	Size         int64  `json:"size"`
	ItemType     string `json:"item_type"`
	Mode         string `json:"mode"`
	LastModified string `json:"last_modified"`
}

type FileInfoRequest struct {
	Files []FileInfo `json:"files"`
}

// File ...
type File struct {
	ID          string `json:"id"`
	Name        string `json:"item_name"`
	Size        int    `json:"size"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	ContentType string `json:"content_type"`
	Etag        string `json:"eTag"`
	RealName    string `json:"real_name"`
}

// FileResponse
type FilesResponse []File

// ChunkRequest
type ChunkRequest struct {
	Length    uint   `json:"length"`
	Offset    uint   `json:"offset"`
	HexSha256 string `json:"hex_sha256"`
}

// ChunkResponse
type ChunkResponse struct {
	ID           string `json:"id"`
	Offset       uint   `json:"offset"`
	Length       uint   `json:"length"`
	HexSha256    string `json:"hex_sha256"`
	PresignedUrl string `json:"presigned_url"`
	Uri          string `json:"uri"`
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

func (c *Client) saveFileInfoPath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/file", recoveryPointID)
}

func (c *Client) saveChunksPath(recoveryPointID string, itemID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/file/%s/chunks", recoveryPointID, itemID)
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

	bufCh := make(chan []byte, 30)
	go func() {
		defer close(bufCh)
		b := make([]byte, MultipartUploadLowerBound)
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
	sem := make(chan struct{}, 15)
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

	return c.CompleteMultipart(ctx, recoveryPointID, m.UploadID)
}

// UploadFile uploads given file to server.
func (c *Client) UploadFile(fn string, r io.Reader, pw io.Writer, batch bool) error {
	if batch {
		return c.uploadMultipart(fn, r, pw)

	}
	return c.uploadFile(fn, r, pw)
}

func (c *Client) SaveFilesInfo(rpID string, dir string) (FilesResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveFileInfoPath(rpID))
	if err != nil {
		return FilesResponse{}, err
	}
	filesInfo, err := Scan(dir)
	if err != nil {
		return FilesResponse{}, err
	}

	req, err := c.NewRequest(http.MethodPost, reqURL, filesInfo.Files)
	if err != nil {
		return FilesResponse{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return FilesResponse{}, err
	}
	defer resp.Body.Close()
	var files FilesResponse

	if err = json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return FilesResponse{}, err
	}
	return files, nil
}

func (c *Client) Chunking(recoveryPointID string, dirPath string, fi File, volume volume.StorageVolume) error {
	//reqURL, err := c.urlStringFromRelPath(c.saveChunksPath(recoveryPointID, fi.ID))
	//if err != nil {
	//	return err
	//}
	file, err := os.Open(filepath.Join(dirPath, fi.RealName))
	if err != nil {
		return err
	}
	chk := chunker.New(file, 0x2b86402d1ae9d5)
	buf := make([]byte, 16*1024*1024)
	fmt.Println("Chunking file", filepath.Join(dirPath, fi.RealName))
	for {
		chunk, err := chk.Next(buf)
		if err == io.EOF {
			break
		}

		if err != nil {
			panic(err)
		}
		//hash := sha256.Sum256(chunk.Data)
		hash := md5.Sum(chunk.Data)
		digest := hex.EncodeToString(hash[:])

		chunkReq := ChunkRequest{
			Length:    chunk.Length,
			Offset:    chunk.Start,
			HexSha256: digest,
		}

		chunkResp, err := c.SaveChunk(recoveryPointID, fi.ID, chunkReq)
		if err != nil {
			return err
		}
		if chunkResp.PresignedUrl != "" {
			volume.SetCredential(chunkResp.PresignedUrl)
		}
		exist, err := volume.TestObject(digest)
		if err != nil {
			return err
		}
		if exist {
			fmt.Println("Object exist in storage", digest)
		} else {
			fmt.Println("Put chunk to storage", digest)
			err = volume.PutObject(digest, chunk.Data)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) SaveChunk(recoveryPointID string, fileID string, chunk ChunkRequest) (ChunkResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.saveChunksPath(recoveryPointID, fileID))
	if err != nil {
		return ChunkResponse{}, err
	}

	req, err := c.NewRequest(http.MethodPost, reqURL, chunk)
	if err != nil {
		return ChunkResponse{}, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return ChunkResponse{}, err
	}
	defer resp.Body.Close()
	var chunkResp ChunkResponse

	if err = json.NewDecoder(resp.Body).Decode(&chunkResp); err != nil {
		return ChunkResponse{}, err
	}
	fmt.Printf("Chunking response %+v\n", chunkResp)
	return chunkResp, nil
}

func Scan(dir string) (FileInfoRequest, error) {
	var fileInfoRequest FileInfoRequest

	err := filepath.Walk(dir, func(path string, fi fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		fileInfo := FileInfo{}

		if fi.IsDir() {
			return nil
		}
		fileInfo.ItemName = path
		fileInfo.Size = fi.Size()
		fileInfo.ItemType = "FILE"
		fileInfo.Mode = "0766"
		fileInfo.LastModified = fi.ModTime().Format("2006-01-02 15:04:05.000000")
		files := fileInfoRequest.Files
		fileInfoRequest.Files = append(files, fileInfo)
		return nil
	})

	return fileInfoRequest, err
}

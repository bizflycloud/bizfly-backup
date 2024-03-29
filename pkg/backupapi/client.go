package backupapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/cenkalti/backoff"
)

const (
	defaultServerURLString = "http://public.vbs.vccloud.vn/v1"
	userAgent              = "bizfly-backup-client"
	latestVersionPath      = "/dashboard/download-urls"
	maxRetry               = 3 * time.Minute
)

// Client is the client for interacting with BackupService API server.
type Client struct {
	client       *http.Client
	ServerURL    *url.URL
	Id           string
	accessKey    string
	secretKey    string
	numGoroutine int

	userAgent string

	logger *zap.Logger
}

// NewClient creates a Client with given options.
func NewClient(opts ...ClientOption) (*Client, error) {
	serverUrl, _ := url.Parse(defaultServerURLString)
	c := &Client{
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 2 * time.Minute,
				ExpectContinueTimeout: 1 * time.Second,
			},
			Timeout: 2 * time.Minute,
		},
		ServerURL: serverUrl,
		userAgent: userAgent,
	}

	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}

	if c.logger == nil {
		l, err := WriteLog()
		if err != nil {
			return nil, err
		}
		c.logger = l
	}

	return c, nil
}

// ClientOption provides mechanism to configure Client.
type ClientOption func(c *Client) error

// WithHTTPClient sets the underlying HTTP client for Client.
func WithHTTPClient(client *http.Client) func(*Client) error {
	return func(c *Client) error {
		if client == nil {
			return errors.New("nil HTTP client")
		}
		c.client = client
		return nil
	}
}

// WithServerURL sets the server url for Client.
func WithServerURL(serverURL string) ClientOption {
	return func(c *Client) error {
		su, err := url.Parse(serverURL)
		if err != nil {
			return err
		}
		c.ServerURL = su
		return nil
	}
}

// WithMachineID sets the machine id for Client.
func WithID(id string) ClientOption {
	return func(c *Client) error {
		c.Id = id
		return nil
	}
}

// WithAccessKey sets the access key for Client.
func WithAccessKey(accessKey string) ClientOption {
	return func(c *Client) error {
		c.accessKey = accessKey
		return nil
	}
}

// WithSecretKey sets the secret key for Client.
func WithSecretKey(secretKey string) ClientOption {
	return func(c *Client) error {
		c.secretKey = secretKey
		return nil
	}
}

// WithNumGoroutine sets the secret key for Client.
func WithNumGoroutine(num int) ClientOption {
	return func(c *Client) error {
		c.numGoroutine = num
		return nil
	}
}

// NewRequest create new http request
func (c *Client) NewRequest(method, relPath string, body interface{}) (*http.Request, error) {
	buf := new(bytes.Buffer)
	if body != nil {
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return nil, err
		}
	}

	reqURl, err := c.urlStringFromRelPath(relPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, reqURl, buf)
	if err != nil {
		return nil, err
	}

	return req, nil
}

// Do makes an http request.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	var err error
	var resp *http.Response

	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry

	body, err := ioutil.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	for {
		req.Body = ioutil.NopCloser(bytes.NewBuffer(body))
		resp, err = c.do(c.client, req, "application/json")
		if err == nil {
			if resp.StatusCode < 400 || resp.StatusCode == 404 {
				return resp, nil
			}
			c.logger.Error("Request StatusCode ", zap.Int("StatusCode", resp.StatusCode))
		} else {
			c.logger.Error("Request error ", zap.Error(err))
		}
		c.logger.Debug("Do http request error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			c.logger.Debug("Do http request error. Retry time out")
			break
		}
		c.logger.Sugar().Info("Do http request error. Retry in ", d)
		time.Sleep(d)
	}

	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		var b bytes.Buffer
		_, _ = io.Copy(&b, resp.Body)
		c.logger.Error("Request error ", zap.Int("StatusCode", resp.StatusCode), zap.String("Body Response", b.String()))
		return nil, fmt.Errorf(fmt.Sprintf("StatusCode %d Body response %s", resp.StatusCode, b.String()))
	}

	return resp, nil
}

func (c *Client) do(httpClient *http.Client, req *http.Request, contentType string) (*http.Response, error) {
	req.Header.Del("Date")
	req.Header.Del("Authorization")
	req.Header.Del("Content-Type")
	req.Header.Add("User-Agent", c.userAgent)
	now := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Add("Date", now)
	req.Header.Add("Authorization", c.authorizationHeaderValue(req.Method, now))
	req.Header.Add("Content-Type", contentType)
	return httpClient.Do(req)
}

func (c *Client) authorizationHeaderValue(method, now string) string {
	s := strings.Join([]string{method, c.accessKey, c.secretKey, now}, "")
	hash := sha256.Sum256([]byte(s))
	return "VBS " + strings.Join([]string{c.accessKey, hex.EncodeToString(hash[:])}, ":")
}

type Version struct {
	Ver     string            `json:"lastest_version"`
	Linux   map[string]string `json:"linux"`
	Macos   map[string]string `json:"macos"`
	Windows map[string]string `json:"windows"`
}

func (c *Client) LatestVersion() (*Version, error) {
	req, err := c.NewRequest(http.MethodGet, latestVersionPath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v Version
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

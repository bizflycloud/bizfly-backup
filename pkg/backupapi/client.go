package backupapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	defaultServerURLString = "http://public.vbs.vccloud.vn/v1"
	userAgent              = "bizfly-backup-client"
)

// Client is the client for interacting with BackupService API server.
type Client struct {
	client    *http.Client
	ServerURL *url.URL
	accessKey string
	secretKey string

	userAgent string
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
				ResponseHeaderTimeout: 10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
		ServerURL: serverUrl,
		userAgent: userAgent,
	}

	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
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

// NewRequest create new http request
func (c *Client) NewRequest(method, relPath string, body interface{}) (*http.Request, error) {
	if c.ServerURL.Path != "" && c.ServerURL.Path != "/" {
		relPath = path.Join(c.ServerURL.Path, relPath)
	}
	relURL, err := url.Parse(relPath)
	if err != nil {
		return nil, err
	}

	u := c.ServerURL.ResolveReference(relURL)

	buf := new(bytes.Buffer)
	if body != nil {
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(method, u.String(), buf)
	if err != nil {
		return nil, err
	}

	return req, nil
}

// Do makes an http request.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.do(c.client, req, "application/json")
}

func (c *Client) do(httpClient *http.Client, req *http.Request, contentType string) (*http.Response, error) {
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

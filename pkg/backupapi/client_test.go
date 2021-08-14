package backupapi

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	client *Client
	mux    *http.ServeMux
	server *httptest.Server
)

func setUp() {
	mux = http.NewServeMux()
	server = httptest.NewServer(mux)

	client, _ = NewClient()
	serverURL, _ := url.Parse(server.URL)
	serverURL.Path = "/api/v1"
	client.ServerURL = serverURL
}

func tearDown() {
	server.Close()
}

func TestClientOptions(t *testing.T) {
	tests := []struct {
		name       string
		opt        ClientOption
		wantErr    bool
		assertFunc func(c *Client) bool
	}{
		{"valid http client", WithHTTPClient(http.DefaultClient), false, func(c *Client) bool { return c.client == http.DefaultClient }},
		{"nil http client", WithHTTPClient(nil), true, nil},
		{"valid server url", WithServerURL("https://foo.bar/api/v1"), false, func(c *Client) bool { return c.ServerURL.Host == "foo.bar" && c.ServerURL.Path == "/api/v1" }},
		{"invalid server url", WithServerURL("https://:foo.bar/api/v1"), true, nil},
		{"access key", WithAccessKey("access_key"), false, func(c *Client) bool { return c.accessKey == "access_key" }},
		{"secret key", WithSecretKey("secret_key"), false, func(c *Client) bool { return c.secretKey == "secret_key" }},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewClient(tc.opt)
			requireFunc := require.NoError
			if tc.wantErr {
				requireFunc = require.Error
			}
			requireFunc(t, err)
			if tc.assertFunc != nil {
				assert.True(t, tc.assertFunc(c))
			}
		})
	}
}

func TestDo(t *testing.T) {
	setUp()
	defer tearDown()

	mux.HandleFunc("/api/v1", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "bizfly-backup-client", r.Header.Get("User-Agent"))
		assert.NotEmpty(t, r.Header.Get("Date"))
		authorizationHeaderValue := r.Header.Get("Authorization")
		assert.Equal(t, "VBS ", authorizationHeaderValue[:4])
		// Authorization header value hash prefix "VBS ", so length must greater than 4
		assert.Greater(t, len(authorizationHeaderValue), 4)
		t.Log(authorizationHeaderValue)
		_, _ = w.Write([]byte("foo"))
	})

	client.accessKey = "access_key"
	client.secretKey = "secret_key"
	req, _ := client.NewRequest("GET", "/", nil)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do(): %v", err)
	}
	require.Nil(t, checkResponse(resp))
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Do(): %v", err)
	}

	res := string(body)
	expected := "foo"
	if !reflect.DeepEqual(res, expected) {
		t.Fatalf("Expected %v - Got %v", expected, res)
	}
}

var latestVer = `
{
    "lastest_version": "0.0.8",
    "linux": {
        "386": "https://github.com/bizflycloud/bizfly-backup/releases/download/v0.0.8/bizfly-backup_linux_386.tar.gz",
        "amd64": "https://github.com/bizflycloud/bizfly-backup/releases/download/v0.0.8/bizfly-backup_linux_amd64.tar.gz",
        "arm64": "https://github.com/bizflycloud/bizfly-backup/releases/download/v0.0.8/bizfly-backup_linux_arm64.tar.gz",
        "armv6": "https://github.com/bizflycloud/bizfly-backup/releases/download/v0.0.8/bizfly-backup_linux_armv6.tar.gz"
    },
    "macos": {
        "amd64": "https://github.com/bizflycloud/bizfly-backup/releases/download/v0.0.8/bizfly-backup_darwin_amd64.tar.gz"
    },
    "windows": {
        "386": "https://github.com/bizflycloud/bizfly-backup/releases/download/v0.0.8/bizfly-backup_windows_386.zip",
        "amd64": "https://github.com/bizflycloud/bizfly-backup/releases/download/v0.0.8/bizfly-backup_windows_amd64.zip"
    }
}
`

func TestClient_LatestVersion(t *testing.T) {
	setUp()
	defer tearDown()

	mux.HandleFunc(path.Join("/api/v1", latestVersionPath), func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(latestVer))
	})
	lv, err := client.LatestVersion()
	assert.NoError(t, err)
	assert.Equal(t, "0.0.8", lv.Ver)
	assert.Len(t, lv.Linux, 4)
	assert.Len(t, lv.Macos, 1)
	assert.Len(t, lv.Windows, 2)
}

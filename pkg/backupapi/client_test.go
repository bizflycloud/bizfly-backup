package backupapi

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
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

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "bizfly-backup-client", r.Header.Get("User-Agent"))
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

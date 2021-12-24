package limiter

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimiterWrapping(t *testing.T) {
	reader := bytes.NewReader([]byte{})
	writer := new(bytes.Buffer)

	for _, limits := range []struct {
		upstream   int
		downstream int
	}{
		{0, 0},
		{42, 0},
		{0, 42},
		{42, 42},
	} {
		limiter := NewStaticLimiter(limits.upstream*1024, limits.downstream*1024)

		mustWrapUpstream := limits.upstream > 0
		assert.Equal(t, limiter.Upstream(reader) != reader, mustWrapUpstream)
		assert.Equal(t, limiter.UpstreamWriter(writer) != writer, mustWrapUpstream)

		mustWrapDownstream := limits.downstream > 0
		assert.Equal(t, limiter.Downstream(reader) != reader, mustWrapDownstream)
		assert.Equal(t, limiter.DownstreamWriter(writer) != writer, mustWrapDownstream)
	}
}

type tracedReadCloser struct {
	io.Reader
	Closed bool
}

func newTracedReadCloser(rd io.Reader) *tracedReadCloser {
	return &tracedReadCloser{Reader: rd}
}

func (r *tracedReadCloser) Close() error {
	r.Closed = true
	return nil
}

func TestRoundTripperReader(t *testing.T) {
	limiter := NewStaticLimiter(42*1024, 42*1024)
	data := make([]byte, 1234)
	_, err := io.ReadFull(rand.Reader, data)
	require.NoError(t, err)

	var send *tracedReadCloser = newTracedReadCloser(bytes.NewReader(data))
	var recv *tracedReadCloser

	rt := limiter.Transport(roundTripper(func(req *http.Request) (*http.Response, error) {
		buf := new(bytes.Buffer)
		_, err := io.Copy(buf, req.Body)
		if err != nil {
			return nil, err
		}
		err = req.Body.Close()
		if err != nil {
			return nil, err
		}

		recv = newTracedReadCloser(bytes.NewReader(buf.Bytes()))
		return &http.Response{Body: recv}, nil
	}))

	res, err := rt.RoundTrip(&http.Request{Body: send})
	require.NoError(t, err)

	out := new(bytes.Buffer)
	n, err := io.Copy(out, res.Body)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), n)
	require.NoError(t, res.Body.Close())

	assert.True(t, send.Closed, "request body not closed")
	assert.True(t, recv.Closed, "result body not closed")
	assert.True(t, bytes.Equal(data, out.Bytes()), "data ping-pong failed")
}

func TestRoundTripperCornerCases(t *testing.T) {
	limiter := NewStaticLimiter(100, 100)

	rt := limiter.Transport(roundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{}, nil
	}))

	res, err := rt.RoundTrip(&http.Request{})
	require.NoError(t, err)
	assert.True(t, res != nil, "round tripper returned no response")

	rt = limiter.Transport(roundTripper(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("error")
	}))

	_, err = rt.RoundTrip(&http.Request{})
	assert.True(t, err != nil, "round tripper lost an error")
}

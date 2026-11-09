package backupapi

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProgressWriter(t *testing.T) {
	buf := new(bytes.Buffer)
	pw := NewProgressWriter(buf)
	r := bytes.NewBufferString("123")
	_, _ = io.Copy(io.Discard, io.TeeReader(r, pw))
	assert.Equal(t, "\r                    \rTotal: 3 B done", buf.String())
}

func TestGetOutboundIP(t *testing.T) {
	ip := getOutboundIP()
	assert.NotEmpty(t, ip)
}

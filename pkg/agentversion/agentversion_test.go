package agentversion

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersion(t *testing.T) {
	assert.Contains(t, Version(), "version: dev,")
}

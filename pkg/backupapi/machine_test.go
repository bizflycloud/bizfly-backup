package backupapi

import (
	"encoding/json"
	"net/http"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_UpdateMachine(t *testing.T) {
	setUp()
	defer tearDown()

	mux.HandleFunc(path.Join("/api/v1", updateMachinePath), func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "bizfly-backup-client", r.Header.Get("User-Agent"))

		var m Machine
		require.NoError(t, json.NewDecoder(r.Body).Decode(&m))
		assert.NotEmpty(t, m.HostName)
		assert.NotEmpty(t, m.OSVersion)
		assert.NotEmpty(t, m.AgentVersion)
		assert.NotEmpty(t, m.OSMachineID)
		_, _ = w.Write([]byte(""))
	})

	assert.NoError(t, client.UpdateMachine())
}

package backupapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const configContent = `
backup-directories:
- activated: true

  id: 6dd19ea8-a690-4fa0-8935-2b04f3c663ef

  name: backup images

  path: home/ducpx/images

  policies:

  - id: a48cfe94-a4f6-4689-9a6d-e94654cda08a

    name: null

    schedule_pattern: '***'

- activated: false

  id: dbf88cc0-947d-493f-8cb4-44dfefaa0628

  name: backup video

  path: home/ducpx/video

  policies:

  - id: c9312fff-457b-4e4b-8703-139c270a53ce

    name: backup daily

    schedule_pattern: '***'
`

func TestClient_GetConfig(t *testing.T) {
	setUp()
	defer tearDown()

	mux.HandleFunc("/agent/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/yaml")
		_, _ = w.Write([]byte(configContent))
	})

	cfg, err := client.GetConfig(context.Background())
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Len(t, cfg.BackupDirectories, 2)
	for _, bd := range cfg.BackupDirectories {
		assert.NotEmpty(t, bd.ID)
		assert.NotEmpty(t, bd.Name)
		assert.NotEmpty(t, bd.Path)
		assert.Len(t, bd.Policies, 1)
	}
}

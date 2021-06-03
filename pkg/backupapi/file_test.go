package backupapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClient_saveFileInfoPath(t *testing.T) {
	setUp()
	defer tearDown()

	recoveryPointID := "recovery-point-id"
	sfip := client.saveFileInfoPath(recoveryPointID)
	assert.Equal(t, "/agent/recovery-points/recovery-point-id/file", sfip)
}

// func TestClient_GetListFilePath(t *testing.T) {
// 	setUp()
// 	defer tearDown()

// 	recoveryPointID := "recovery-point-id"
// 	scp := client.getListFilePath(recoveryPointID)

// 	mux.HandleFunc(path.Join("/api/v1/", scp), func(w http.ResponseWriter, r *http.Request) {
// 		resp := &RecoveryPointResponse{
// 			Files: []File{
// 				{ID: "1", Etag: "etag", ItemName: "item-name", ItemType: "item-type", Mode: "mode", RealName: "real-name", Size: "0"},
// 				{ID: "2", Etag: "etag", ItemName: "item-name", ItemType: "item-type", Mode: "mode", RealName: "real-name", Size: "1"},
// 			},
// 			Total: "2",
// 		}
// 		assert.NoError(t, json.NewEncoder(w).Encode(resp))
// 	})

// 	rpr, err := client.GetListFilePath(recoveryPointID)
// 	require.NoError(t, err)
// 	assert.Len(t, rpr, 2)
// }

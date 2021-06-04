package backupapi

import (
	"os"
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

func TestClient_getItemLatestPath(t *testing.T) {
	setUp()
	defer tearDown()

	recoveryPointID := "recovery-point-id"
	filePath := "/home/vinh/folder1/file1"
	gilp := client.getItemLatestPath(recoveryPointID, filePath)
	assert.Equal(t, "/agent/recovery-points/recovery-point-id/path?path=/home/vinh/folder1/file1", gilp)
}

func TestClient_CreateFile(t *testing.T) {
	setUp()
	defer tearDown()

	path := "/home/dactoan/upload/pic.jpg"
	file, err := os.Create(path)
	if err != nil {
		t.Error(err)
	}
	defer file.Close()
}

// func TestClient_GetItemLatest(t *testing.T) {
// 	setUp()
// 	defer tearDown()

// 	recoveryPointID := "recovery-point-id"
// 	filePath := "/home/test/folder1/file1"
// 	itemLatestPath := client.getItemLatestPath(recoveryPointID, filePath)

// 	mux.HandleFunc(path.Join("/api/v1/", itemLatestPath), func(w http.ResponseWriter, r *http.Request) {
// 		resp := &ItemInfoLatest{
// 			ID:           "12354954-f1ca-4110-ab22-f4430bf61456",
// 			ChangedTime:  "2021-05-21 17:07:29.053311809",
// 			ModifiedTime: "2021-05-21 17:07:29.053311809",
// 		}
// 		assert.NoError(t, json.NewEncoder(w).Encode(resp))
// 	})

// 	itemLatest, err := client.GetItemLatest(recoveryPointID, filePath)
// 	require.NoError(t, err)
// 	assert.NotEmpty(t, itemLatest.ID)
// 	assert.NotEmpty(t, itemLatest.ChangedTime)
// 	assert.NotEmpty(t, itemLatest.ModifiedTime)
// }

// func TestClient_SaveFileInfo(t *testing.T) {
// 	setUp()
// 	defer tearDown()

// 	recoveryPointID := "recovery-point-id"
// 	saveFileInfo := client.saveFileInfoPath(recoveryPointID)

// 	mux.HandleFunc(path.Join("/api/v1/", saveFileInfo), func(w http.ResponseWriter, r *http.Request) {
// 		file := &File{
// 			ID:          "1",
// 			ContentType: "FILE",
// 			ItemName:    "/home/test/folder1/file1",
// 			RealName:    "file1",
// 			Etag:        "fffa7024280901ea6fa23ac718b41999",
// 		}
// 		require.NoError(t, json.NewEncoder(w).Encode(file))
// 	})

// 	itemLatest, err := client.SaveFileInfo(recoveryPointID, &ItemInfo{
// 		ItemType:    "FILE",
// 		ParentRpID:  "ParentRpID",
// 		RpReference: true,
// 		Attribute: Attribute{
// 			ItemID:       "1",
// 			ItemName:     "item-name",
// 			Size:         "10",
// 			ChangedTime:  "2021-05-21 17:07:29.053311809",
// 			ModifiedTime: "2021-05-21 17:07:29.053311809",
// 			AccessTime:   "2021-05-21 17:07:29.053311809",
// 			Mode:         "--r",
// 			GID:          "1000",
// 			UID:          "1000",
// 		},
// 	})
// 	require.NoError(t, err)
// 	assert.NotEmpty(t, itemLatest.ID)
// }

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

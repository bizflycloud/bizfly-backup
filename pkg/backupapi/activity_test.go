package backupapi

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_activityPath(t *testing.T) {
	setUp()
	defer tearDown()

	lap := client.listActivityPath()
	assert.Equal(t, "/agent/activity", lap)
}

func TestClient_ListActivity(t *testing.T) {
	setUp()
	defer tearDown()

	mux.HandleFunc(path.Join("/api/v1", client.listActivityPath()), func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		resp := `
{
    "activities": [
        {
            "action": "RESTORE",
            "backup_directory_id": null,
            "created_at": "2022-04-21T10:05:53.270057+00:00",
            "extra": null,
            "id": "557b84e9-9444-4379-bf1e-bb8c103f7610",
            "machine_id": "d1bfa61a-b0a6-4e64-b9f7-61d68037693a",
            "message": "Restore backup test1234 from machine test01 to /home/tris/test-backup in machine test01 ",
            "policy_id": null,
            "progress_restore": "25.00%",
            "reason": null,
            "recovery_point": {
                "created_at": "2022-04-21T05:05:55.819261+00:00",
                "failure_reason": null,
                "id": "b069235a-1727-4aef-92f0-edd1611fd08b",
                "index_hash": "69e4ad46316cbafd2cbecc31fd7b3d98f116b3d2ec836d544bca12869ec57337",
                "local_size": 10516924046,
                "name": "test",
                "progress": "100%",
                "recovery_point_type": "INITIAL_REPLICA",
                "status": "COMPLETED",
                "storage_size": 10488816903,
                "total_files": 375,
                "updated_at": "2022-04-21T05:08:56.691504+00:00"
            },
            "status": "DOWNLOADING",
            "tenant_id": "45953a63-4221-4840-837e-531db1f6d47d",
            "updated_at": "2022-04-21T10:19:11.579903+00:00",
            "user_id": "d1bfa61a-b0a6-4e64-b9f7-61d68037693a"
        },
		{
            "action": "BACKUP_MANUAL",
            "backup_directory_id": "f95c09c2-d77b-405e-a68d-37351be3f8d5",
            "created_at": "2022-04-22T02:32:42.576154+00:00",
            "extra": null,
            "id": "cc88c529-5fea-4f10-b3d4-c9cc31085dc9",
            "machine_id": "d1bfa61a-b0a6-4e64-b9f7-61d68037693a",
            "message": "Backup directory C:\\Users\\Administrator\\Desktop in machine test01",
            "policy_id": null,
            "progress_restore": null,
            "reason": null,
            "recovery_point": {
                "created_at": "2022-04-22T02:32:42.512374+00:00",
                "failure_reason": null,
                "id": "44cf97d0-c436-49f3-8985-f14d8eb6c332",
                "index_hash": "4d5d38a38f30b4fdea1ee62127892be1630eca34a85db673369517c5991436a9",
                "local_size": 22286318,
                "name": "test",
                "progress": "25.00%",
                "recovery_point_type": "INITIAL_REPLICA",
                "status": "UPLOADING",
                "storage_size": 22286036,
                "total_files": 3,
                "updated_at": "2022-04-22T02:32:44.635681+00:00"
            },
            "status": "UPLOADING",
            "tenant_id": "45953a63-4221-4840-837e-531db1f6d47d",
            "updated_at": "2022-04-22T02:32:44.635681+00:00",
            "user_id": "d1bfa61a-b0a6-4e64-b9f7-61d68037693a"
        }
    ],
    "total": 2
}
		
`
		_, _ = fmt.Fprint(w, resp)
	})

	ctx := context.Background()
	machineID := "d1bfa61a-b0a6-4e64-b9f7-61d68037693a"
	listStatus := []string{"UPLOADING", "DOWNLOADING"}
	lat, err := client.ListActivity(ctx, machineID, listStatus)
	require.NoError(t, err)
	assert.Equal(t, 2, len(lat.Activities))
	assert.Equal(t, machineID, lat.Activities[0].MachineID)
	assert.Equal(t, machineID, lat.Activities[1].MachineID)
	assert.Contains(t, listStatus, lat.Activities[0].Status)
	assert.Contains(t, listStatus, lat.Activities[1].Status)
}

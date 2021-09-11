package backupapi

import (
	"net/http"
	"net/url"
	"testing"

	"go.uber.org/zap"
)

func TestClient_credentialStorageVaultPath(t *testing.T) {
	type fields struct {
		client    *http.Client
		ServerURL *url.URL
		Id        string
		accessKey string
		secretKey string
		userAgent string
		logger    *zap.Logger
	}
	type args struct {
		storageVaultID string
		actionID       string
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   string
	}{
		{
			name: "test credential storage vault path",
			args: args{
				storageVaultID: "1",
				actionID:       "1",
			},
			want: "/agent/storage_vaults/1/credential?action_id=1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{
				client:    tt.fields.client,
				ServerURL: tt.fields.ServerURL,
				Id:        tt.fields.Id,
				accessKey: tt.fields.accessKey,
				secretKey: tt.fields.secretKey,
				userAgent: tt.fields.userAgent,
				logger:    tt.fields.logger,
			}
			if got := c.credentialStorageVaultPath(tt.args.storageVaultID, tt.args.actionID); got != tt.want {
				t.Errorf("Client.credentialStorageVaultPath() = %v, want %v", got, tt.want)
			}
		})
	}
}

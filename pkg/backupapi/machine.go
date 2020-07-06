package backupapi

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"

	"github.com/favadi/osinfo"

	"github.com/bizflycloud/bizfly-backup/pkg/agentversion"
)

const (
	updateMachinePath = "/agent/register"
)

// Machine ...
type Machine struct {
	ID           string `json:"id"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	Name         string `json:"name"`
	HostName     string `json:"host_name"`
	IPAddress    string `json:"ip_address"`
	OSVersion    string `json:"os_version"`
	AgentVersion string `json:"agent_version"`
	TenantID     string `json:"tenant_id"`
}

// UpdateMachine updates machine information.
func (c *Client) UpdateMachine() error {
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("os.Hostname(): %w", err)
	}
	oi, err := osinfo.New()
	if err != nil {
		return fmt.Errorf("osinfo.New(): %w", err)
	}

	m := &Machine{
		HostName:     hostname,
		OSVersion:    oi.String(),
		AgentVersion: agentversion.Version(),
	}

	req, err := c.NewRequest(http.MethodPatch, updateMachinePath, m)
	if err != nil {
		return fmt.Errorf("c.NewRequest(): %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("c.Do(): %w", err)
	}
	defer resp.Body.Close()
	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ioutil.ReadAll(): err")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected error: %s, status: %d", string(buf), resp.StatusCode)
	}
	return nil
}

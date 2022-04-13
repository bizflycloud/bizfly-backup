package backupapi

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/bizflycloud/bizfly-backup/pkg/agentversion"

	"go.uber.org/zap"
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
	OSMachineID  string `json:"os_machine_id"`
}

// UpdateMachineResponse is the server response when update machine info
type UpdateMachineResponse struct {
	BrokerUrl string `json:"broker_url"`
}

// Get OS Name
func osName() string {
	switch runtime.GOOS {
	case "windows":
		command := "systeminfo | findstr /c:'OS Name'"
		name, _ := exec.Command("powershell", "-Command", command).Output()
		osName := strings.Split(string(name), ":")
		return strings.Replace(osName[1], "Microsoft ", "", -1)
	case "darwin":
		out, _ := exec.Command("bash", "-c", "sw_vers -productName").Output()
		names := strings.Split(string(out), "\n")
		version, _ := exec.Command("bash", "-c", "sw_vers -productVersion").Output()
		return names[0] + " " + string(version)
	case "linux":
		name, _ := exec.Command("bash", "-c", `. /etc/os-release; echo "$PRETTY_NAME"`).Output()
		return string(name)
	default:
		return ""
	}
}

// UpdateMachine updates machine information.
func (c *Client) UpdateMachine() (*UpdateMachineResponse, error) {
	hostname, err := os.Hostname()
	if err != nil {
		c.logger.Error("os.Hostname() ", zap.Error(err))
		return nil, err
	}
	m := &Machine{
		HostName:     hostname,
		OSVersion:    osName(),
		AgentVersion: agentversion.Version(),
		IPAddress:    getOutboundIP(),
	}

	req, err := c.NewRequest(http.MethodPatch, updateMachinePath, m)
	if err != nil {
		c.logger.Error("c.NewRequest() ", zap.Error(err))
		return nil, err
	}

	resp, err := c.Do(req)
	if err != nil {
		c.logger.Error("c.Do() ", zap.Error(err))
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()
	var umr UpdateMachineResponse
	if err := json.NewDecoder(resp.Body).Decode(&umr); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("ioutil.ReadAll() ", zap.Error(err))
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		c.logger.Error("err ", zap.Error(err), zap.String("unexpected error", string(buf)), zap.Int("status", resp.StatusCode))
		return nil, err
	}
	return &umr, nil
}

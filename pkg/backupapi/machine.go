package backupapi

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"

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
	OSMachineID  string `json:"os_machine_id"`
}

// UpdateMachineResponse is the server response when update machine info
type UpdateMachineResponse struct {
	BrokerUrl string `json:"broker_url"`
}

// Get OS Name
func os_name() string {
	os_type := runtime.GOOS
	switch os_type {
	case "windows":
		command := "(Get-ComputerInfo).WindowsProductName"
		os_name, _ := exec.Command("powershell", "-Command", command).Output()
		return string(os_name)
	case "darwin":
		out, _ := exec.Command("bash", "-c", "sw_vers -productName").Output()
		os_name := strings.Split(string(out), "\n")
		os_version, _ := exec.Command("bash", "-c", "sw_vers -productVersion").Output()
		return (os_name[0] + " " + string(os_version))
	case "linux":
		os_name, _ := exec.Command("bash", "-c", ". /etc/os-release; echo $PRETTY_NAME").Output()
		return string(os_name)
	default:
		return string(os_type)
	}
}

// UpdateMachine updates machine information.
func (c *Client) UpdateMachine() (*UpdateMachineResponse, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("os.Hostname(): %w", err)
	}
	os_name := os_name()
	m := &Machine{
		HostName:     hostname,
		OSVersion:    os_name,
		AgentVersion: agentversion.Version(),
		IPAddress:    getOutboundIP(),
	}

	req, err := c.NewRequest(http.MethodPatch, updateMachinePath, m)
	if err != nil {
		return nil, fmt.Errorf("c.NewRequest(): %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("c.Do(): %w", err)
	}
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var umr UpdateMachineResponse
	if err := json.NewDecoder(resp.Body).Decode(&umr); err != nil {
		return nil, err
	}

	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ioutil.ReadAll(): err")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected error: %s, status: %d", string(buf), resp.StatusCode)
	}
	return &umr, nil
}

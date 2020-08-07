package backupapi

import (
	"context"
	"net/http"

	"gopkg.in/yaml.v2"
)

// Config is the scheduler config for machine.
type Config struct {
	BackupDirectories []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Path     string `json:"path"`
		Policies []struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			SchedulePattern string `json:"schedule_pattern"`
			Activated       bool   `json:"activated"`
		} `json:"policies"`
	} `json:"backup_directories"`
}

func (c *Client) configPath() string {
	return "/agent/config"
}

func (c *Client) GetConfig(ctx context.Context) (*Config, error) {
	req, err := c.NewRequest(http.MethodGet, c.configPath(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var cfg Config
	if err := yaml.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

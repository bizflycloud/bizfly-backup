package backupapi

import (
	"context"
	"net/http"

	"go.uber.org/zap"
	"gopkg.in/yaml.v2"
)

// BackupDirectoryConfig is the cron policies for given directory.
type BackupDirectoryConfig struct {
	ID        string                        `json:"id" yaml:"id"`
	Name      string                        `json:"name" yaml:"name"`
	Path      string                        `json:"path" yaml:"path"`
	Policies  []BackupDirectoryConfigPolicy `json:"policies" yaml:"policies"`
	Activated bool                          `json:"activated" yaml:"activated"`
}

// BackupDirectoryConfigPolicy is the cron policy.
type BackupDirectoryConfigPolicy struct {
	ID              string `json:"id" yaml:"id"`
	Name            string `json:"name" yaml:"name"`
	SchedulePattern string `json:"schedule_pattern" yaml:"schedule_pattern"`
	Retentions      string `json:"retentions" yaml:"retentions"`
	LimitUpload     int    `json:"limit_upload" yaml:"limit_upload"`
}

type Config struct {
	BackupDirectories []BackupDirectoryConfig `json:"backup_directories" yaml:"backup_directories"`
}

func (c *Client) configPath() string {
	return "/agent/config"
}

func (c *Client) GetConfig(ctx context.Context) (*Config, error) {
	req, err := c.NewRequest(http.MethodGet, c.configPath(), nil)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()

	var cfg Config
	if err := yaml.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	return &cfg, nil
}

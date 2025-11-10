package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config stores the agent configuration
type Config struct {
	Endpoint           string `json:"endpoint"`
	AccessKeyID        string `json:"access_key_id"`
	SecretAccessKey    string `json:"secret_access_key"`
	UseSSL             bool   `json:"use_ssl"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	CachePath          string `json:"cache_path"`
	MountPath          string `json:"mount_path"`
}

// GetConfigPath returns the configuration file path
func GetConfigPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	configDir := filepath.Join(homeDir, ".maxiofs-agent")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(configDir, "config.json"), nil
}

// Load loads configuration from disk
func Load() (*Config, error) {
	configPath, err := GetConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Retornar config por defecto si no existe
			return &Config{
				UseSSL:    true,
				CachePath: filepath.Join(filepath.Dir(configPath), "cache"),
				MountPath: "",
			}, nil
		}
		return nil, err
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// Save saves configuration to disk
func (c *Config) Save() error {
	configPath, err := GetConfigPath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

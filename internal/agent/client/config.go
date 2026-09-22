package client

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	Hub   string `json:"hub"`
	Token string `json:"token"`
	Name  string `json:"name"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// SaveConfig 以 0600 写入，先写临时文件再 rename：token 不会以部分写入的状态落盘。
func SaveConfig(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

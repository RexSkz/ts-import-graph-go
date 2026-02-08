package tsconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type CompilerOptions struct {
	Paths   map[string][]string `json:"paths"`
	RootDir string              `json:"rootDir"`
	BaseURL string              `json:"baseUrl"`
}

type Config struct {
	CompilerOptions CompilerOptions `json:"compilerOptions"`
}

func Load(root string) (map[string][]string, string) {
	tsconfigPath := filepath.Join(root, "tsconfig.json")
	data, err := os.ReadFile(tsconfigPath)
	if err != nil {
		return nil, ""
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, ""
	}

	baseURL := config.CompilerOptions.BaseURL
	if baseURL == "" {
		baseURL = "."
	}

	return config.CompilerOptions.Paths, baseURL
}

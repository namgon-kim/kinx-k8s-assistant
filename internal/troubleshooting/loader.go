package troubleshooting

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type runbookFile struct {
	Cases []TroubleshootingCase `yaml:"cases"`
}

func LoadRunbooks(dir string) ([]TroubleshootingCase, error) {
	if dir == "" {
		dir = DefaultRunbookDir()
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var cases []TroubleshootingCase
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		loaded, err := loadRunbookFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		cases = append(cases, loaded...)
	}

	if len(cases) == 0 {
		return nil, fmt.Errorf("no runbook cases found in %s", dir)
	}
	return cases, nil
}

func DefaultRunbookDir() string {
	candidates := []string{
		filepath.Join("internal", "troubleshooting", "runbooks"),
		filepath.Join("..", "internal", "troubleshooting", "runbooks"),
		filepath.Join("..", "..", "internal", "troubleshooting", "runbooks"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

func loadRunbookFile(path string) ([]TroubleshootingCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var wrapper runbookFile
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}

	for i := range wrapper.Cases {
		if wrapper.Cases[i].Source == "" {
			wrapper.Cases[i].Source = path
		}
	}
	return wrapper.Cases, nil
}

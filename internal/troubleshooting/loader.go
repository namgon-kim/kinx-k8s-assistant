package troubleshooting

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
	"gopkg.in/yaml.v3"
)

//go:embed runbooks/*.yaml
var embeddedRunbookFS embed.FS

type runbookFile struct {
	Cases []TroubleshootingCase `yaml:"cases"`
}

func LoadRunbooks(dir string) ([]TroubleshootingCase, error) {
	useDefault := dir == ""
	if dir == "" {
		dir = DefaultRunbookDir()
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if useDefault || os.IsNotExist(err) {
			return loadEmbeddedRunbooks()
		}
		return nil, err
	}

	var cases []TroubleshootingCase
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if isDraftRunbook(name) {
			continue
		}
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
		if useDefault {
			return loadEmbeddedRunbooks()
		}
		return nil, fmt.Errorf("no runbook cases found in %s", dir)
	}
	return cases, nil
}

func loadEmbeddedRunbooks() ([]TroubleshootingCase, error) {
	entries, err := embeddedRunbookFS.ReadDir("runbooks")
	if err != nil {
		return nil, err
	}
	var cases []TroubleshootingCase
	for _, entry := range entries {
		if entry.IsDir() || isDraftRunbook(entry.Name()) || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}
		path := filepath.Join("runbooks", entry.Name())
		data, err := embeddedRunbookFS.ReadFile(path)
		if err != nil {
			return nil, err
		}
		loaded, err := parseRunbookData(data, "embedded:"+path)
		if err != nil {
			return nil, err
		}
		cases = append(cases, loaded...)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("no embedded runbook cases found")
	}
	return cases, nil
}

func isDraftRunbook(name string) bool {
	lower := strings.ToLower(name)
	return lower == "draft.yaml" || lower == "draft.yml" || strings.HasPrefix(lower, "draft.")
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
	return parseRunbookData(data, path)
}

func parseRunbookData(data []byte, source string) ([]TroubleshootingCase, error) {
	var wrapper runbookFile
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}

	for i := range wrapper.Cases {
		if wrapper.Cases[i].Source == "" {
			wrapper.Cases[i].Source = source
		}
	}
	return wrapper.Cases, nil
}

func (c *TroubleshootingCase) UnmarshalYAML(value *yaml.Node) error {
	var aux struct {
		ID               string      `yaml:"id"`
		Title            string      `yaml:"title"`
		MatchTypes       []yaml.Node `yaml:"match_types"`
		Symptoms         []string    `yaml:"symptoms"`
		EvidenceKeywords []string    `yaml:"evidence_keywords"`
		Similarity       float64     `yaml:"similarity"`
		Cause            string      `yaml:"cause"`
		LikelyCauses     []string    `yaml:"likely_causes"`
		Resolution       string      `yaml:"resolution"`
		DecisionHints    []string    `yaml:"decision_hints"`
		RelatedObjects   []string    `yaml:"related_objects"`
		DiagnosticSteps  []PlanStep  `yaml:"diagnostic_steps"`
		RemediateSteps   []PlanStep  `yaml:"remediate_steps"`
		VerifySteps      []PlanStep  `yaml:"verify_steps"`
		RollbackSteps    []PlanStep  `yaml:"rollback_steps"`
		RiskLevel        RiskLevel   `yaml:"risk_level"`
		Source           string      `yaml:"source"`
		Tags             []string    `yaml:"tags"`
	}
	if err := value.Decode(&aux); err != nil {
		return err
	}
	*c = TroubleshootingCase{
		ID:               aux.ID,
		Title:            aux.Title,
		Symptoms:         aux.Symptoms,
		EvidenceKeywords: aux.EvidenceKeywords,
		Similarity:       aux.Similarity,
		Cause:            aux.Cause,
		LikelyCauses:     aux.LikelyCauses,
		Resolution:       aux.Resolution,
		DecisionHints:    aux.DecisionHints,
		RelatedObjects:   aux.RelatedObjects,
		DiagnosticSteps:  aux.DiagnosticSteps,
		RemediateSteps:   aux.RemediateSteps,
		VerifySteps:      aux.VerifySteps,
		RollbackSteps:    aux.RollbackSteps,
		RiskLevel:        aux.RiskLevel,
		Source:           aux.Source,
		Tags:             aux.Tags,
	}
	for _, node := range aux.MatchTypes {
		text := yamlNodeText(node)
		if text == "" {
			continue
		}
		c.MatchTypes = append(c.MatchTypes, diagnostic.DetectionType(text))
	}
	return nil
}

func yamlNodeText(node yaml.Node) string {
	switch node.Kind {
	case yaml.ScalarNode:
		return strings.TrimSpace(node.Value)
	case yaml.MappingNode:
		if len(node.Content) >= 2 {
			return strings.TrimSpace(node.Content[0].Value + ": " + node.Content[1].Value)
		}
	}
	return ""
}

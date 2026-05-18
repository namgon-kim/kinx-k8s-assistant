package guidance

import (
	"testing"

	appconfig "github.com/namgon-kim/kinx-k8s-assistant/internal/config"
)

func TestClientsUseConfiguredCollections(t *testing.T) {
	appCfg := &appconfig.Config{
		Guidance: appconfig.GuidanceConfig{
			ResourceGuides: "resource-guides",
			IncidentGuides: "incident-guides",
		},
	}

	incident, err := NewIncidentClient(appCfg)
	if err != nil {
		t.Fatalf("new incident client: %v", err)
	}
	if incident.cfg.QdrantCollection != "incident-guides" {
		t.Fatalf("incident collection = %q", incident.cfg.QdrantCollection)
	}

	resource, err := NewResourceGuideClient(appCfg)
	if err != nil {
		t.Fatalf("new resource client: %v", err)
	}
	if resource.cfg.QdrantCollection != "resource-guides" {
		t.Fatalf("resource collection = %q", resource.cfg.QdrantCollection)
	}
}

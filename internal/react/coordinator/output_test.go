package coordinator

import (
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/language"
)

func TestTranslateModelTextSkipsWhenConfigSwitchedToEnglish(t *testing.T) {
	// Simulate /lang switching after the Korean translator was initialized.
	translator := language.New(&config.Config{
		Lang: config.LangConfig{
			Language: "Korean",
			Model:    "translator-model",
			Endpoint: "http://translator.example",
		},
	})
	if translator == nil {
		t.Fatal("expected non-nil Korean translator")
	}

	loop := &Loop{
		cfg: &config.Config{Lang: config.LangConfig{
			Language: "English",
			Model:    "translator-model",
			Endpoint: "http://translator.example",
		}},
		lang: translator,
	}

	got := loop.translateModelText(t.Context(), "Hello world")
	if got != "Hello world" {
		t.Fatalf("translateModelText = %q; want original text after language switch to English", got)
	}
}

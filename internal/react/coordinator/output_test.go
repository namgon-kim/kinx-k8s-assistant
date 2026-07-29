package coordinator

import (
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/language"
)

func TestEmitMessageWithoutOutputChannelReturns(t *testing.T) {
	done := make(chan struct{})
	go func() {
		(&Loop{}).emitMessage(api.MessageSourceAgent, api.MessageTypeError, "test")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("emitMessage blocked on an unavailable output channel")
	}
}

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

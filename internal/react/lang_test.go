package react

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
)

func TestLangTranslatorDisabledForEnglish(t *testing.T) {
	translator := newLangTranslator(&config.Config{
		Lang: config.LangConfig{
			Language: "English",
			Model:    "translator",
			Endpoint: "http://example.invalid",
		},
	})
	if translator != nil {
		t.Fatal("English language should not create translator")
	}
}

func TestLangTranslatorTranslate(t *testing.T) {
	var receivedModel string
	translator := newLangTranslator(&config.Config{
		Lang: config.LangConfig{
			Language: "Korean",
			Model:    "translator-model",
			Endpoint: "http://translator.example",
			APIKey:   "test-key",
		},
	})
	translator.client = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/v1/chat/completions" {
				t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Fatalf("Authorization = %q", got)
			}
			var req openAIChatCompletionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			receivedModel = req.Model
			if req.MaxTokens != 0 {
				t.Fatalf("max_tokens should be omitted, got %d", req.MaxTokens)
			}
			var body bytes.Buffer
			_ = json.NewEncoder(&body).Encode(openAIChatCompletionResponse{
				Choices: []struct {
					Message openAIChatMessage `json:"message"`
				}{
					{Message: openAIChatMessage{Role: "assistant", Content: "번역 결과"}},
				},
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(&body),
				Header:     make(http.Header),
			}, nil
		}),
	}

	got, err := translator.translate(context.Background(), "English answer")
	if err != nil {
		t.Fatal(err)
	}
	if got != "번역 결과" {
		t.Fatalf("translate() = %q", got)
	}
	if receivedModel != "translator-model" {
		t.Fatalf("model = %q", receivedModel)
	}
}

func TestChatCompletionsURLAcceptsVersionedEndpoint(t *testing.T) {
	got := chatCompletionsURL("http://1.201.177.120:4000/v1")
	want := "http://1.201.177.120:4000/v1/chat/completions"
	if got != want {
		t.Fatalf("chatCompletionsURL() = %q, want %q", got, want)
	}
}

func TestChatCompletionsURLAddsVersion(t *testing.T) {
	got := chatCompletionsURL("http://1.201.177.120:4000")
	want := "http://1.201.177.120:4000/v1/chat/completions"
	if got != want {
		t.Fatalf("chatCompletionsURL() = %q, want %q", got, want)
	}
}

func TestChatCompletionsURLAcceptsFullEndpoint(t *testing.T) {
	got := chatCompletionsURL("http://1.201.177.120:4000/v1/chat/completions")
	want := "http://1.201.177.120:4000/v1/chat/completions"
	if got != want {
		t.Fatalf("chatCompletionsURL() = %q, want %q", got, want)
	}
}

func TestTranslateModelTextSkipsWhenConfigSwitchedToEnglish(t *testing.T) {
	// Simulate a loop whose lang was created in Korean mode but cfg.Lang.Language
	// has since been switched to English (e.g. via /lang en).
	koreanCfg := &config.Config{
		Lang: config.LangConfig{
			Language: "Korean",
			Model:    "translator-model",
			Endpoint: "http://translator.example",
		},
	}
	translator := newLangTranslator(koreanCfg)
	if translator == nil {
		t.Fatal("expected non-nil Korean translator")
	}

	cfg := &config.Config{
		Lang: config.LangConfig{
			Language: "English", // switched to English after translator was created
			Model:    "translator-model",
			Endpoint: "http://translator.example",
		},
	}
	loop := &Loop{cfg: cfg, lang: translator}

	got := loop.translateModelText(t.Context(), "Hello world")
	if got != "Hello world" {
		t.Fatalf("translateModelText = %q; want original text after language switch to English", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

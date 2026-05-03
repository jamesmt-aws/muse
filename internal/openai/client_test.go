package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkopenai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ellistarn/muse/internal/inference"
)

func TestBuildParamsIgnoresThinkingBudgetForNonReasoningModels(t *testing.T) {
	client := &Client{model: ModelFull}
	opts := inference.Apply([]inference.ConverseOption{inference.WithThinking(16000)})

	params := client.buildParams("system", []inference.Message{{Role: "user", Content: "hello"}}, opts)

	if !params.MaxCompletionTokens.Valid() {
		t.Fatal("MaxCompletionTokens should be set")
	}
	// Non-reasoning models don't get thinking budget added.
	if got, want := params.MaxCompletionTokens.Value, int64(inference.DefaultMaxTokens); got != want {
		t.Fatalf("MaxCompletionTokens = %d, want %d", got, want)
	}
	if params.ReasoningEffort != "" {
		t.Fatalf("ReasoningEffort = %q, want empty for non-reasoning model", params.ReasoningEffort)
	}
}

func TestBuildParamsSetsReasoningEffortForReasoningModels(t *testing.T) {
	client := &Client{model: "o3"}
	opts := inference.Apply([]inference.ConverseOption{inference.WithThinking(8000)})

	params := client.buildParams("system", []inference.Message{{Role: "user", Content: "hello"}}, opts)

	if got, want := params.ReasoningEffort, sdkopenai.ReasoningEffortMedium; got != want {
		t.Fatalf("ReasoningEffort = %q, want %q", got, want)
	}
	if got, want := params.MaxCompletionTokens.Value, int64(inference.DefaultMaxTokens+8000); got != want {
		t.Fatalf("MaxCompletionTokens = %d, want %d", got, want)
	}
}

// captureServer is a stub OpenAI-compatible server that records the last
// chat completion request body and returns a minimal valid response.
func captureServer(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		lastBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"OK"}}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &lastBody
}

func TestSetThinkingByDefaultAddsToMaxCompletionTokens(t *testing.T) {
	srv, lastBody := captureServer(t)
	c, err := NewClient(t.Context(), "test-model",
		option.WithBaseURL(srv.URL+"/"),
		option.WithAPIKey("test"),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.SetThinkingByDefault(2048)

	if _, err := c.ConverseMessages(context.Background(), "system",
		[]inference.Message{{Role: "user", Content: "hi"}},
		inference.WithMaxTokens(1024),
	); err != nil {
		t.Fatalf("ConverseMessages: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(*lastBody), &parsed); err != nil {
		t.Fatalf("parse captured body: %v\n%s", err, *lastBody)
	}
	got, ok := parsed["max_completion_tokens"].(float64)
	if !ok {
		t.Fatalf("max_completion_tokens missing in body: %s", *lastBody)
	}
	if want := float64(1024 + 2048); got != want {
		t.Fatalf("max_completion_tokens = %v, want %v (1024 caller + 2048 deployment)", got, want)
	}
}

func TestSetThinkingByDefaultZeroIsBaseline(t *testing.T) {
	srv, lastBody := captureServer(t)
	c, err := NewClient(t.Context(), "test-model",
		option.WithBaseURL(srv.URL+"/"),
		option.WithAPIKey("test"),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := c.ConverseMessages(context.Background(), "system",
		[]inference.Message{{Role: "user", Content: "hi"}},
		inference.WithMaxTokens(1024),
	); err != nil {
		t.Fatalf("ConverseMessages: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(*lastBody), &parsed); err != nil {
		t.Fatalf("parse captured body: %v\n%s", err, *lastBody)
	}
	got, ok := parsed["max_completion_tokens"].(float64)
	if !ok {
		t.Fatalf("max_completion_tokens missing in body: %s", *lastBody)
	}
	if want := float64(1024); got != want {
		t.Fatalf("max_completion_tokens = %v, want %v (no deployment thinking budget)", got, want)
	}
}

func TestSetThinkingByDefaultComposesWithWithThinking(t *testing.T) {
	srv, lastBody := captureServer(t)
	c, err := NewClient(t.Context(), "o3", // reasoning model so WithThinking takes effect
		option.WithBaseURL(srv.URL+"/"),
		option.WithAPIKey("test"),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.SetThinkingByDefault(2048)

	if _, err := c.ConverseMessages(context.Background(), "system",
		[]inference.Message{{Role: "user", Content: "hi"}},
		inference.WithMaxTokens(1024),
		inference.WithThinking(8192),
	); err != nil {
		t.Fatalf("ConverseMessages: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(*lastBody), &parsed); err != nil {
		t.Fatalf("parse captured body: %v\n%s", err, *lastBody)
	}
	got, ok := parsed["max_completion_tokens"].(float64)
	if !ok {
		t.Fatalf("max_completion_tokens missing in body: %s", *lastBody)
	}
	if want := float64(1024 + 8192 + 2048); got != want {
		t.Fatalf("max_completion_tokens = %v, want %v (caller + WithThinking + deployment)", got, want)
	}
}

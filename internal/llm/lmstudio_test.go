//go:build lmstudio

package llm

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestRealLMStudio talks to a real LM Studio server. It is skipped unless
// LMSTUDIO_BASE_URL is set.
//
//	go test -tags lmstudio ./internal/llm -run TestRealLMStudio -v
func TestRealLMStudio(t *testing.T) {
	base := os.Getenv("LMSTUDIO_BASE_URL")
	if base == "" {
		t.Skip("LMSTUDIO_BASE_URL not set")
	}
	model := os.Getenv("LMSTUDIO_MODEL")
	if model == "" {
		model = "google/gemma-4-26b-a4b"
	}
	c := New(base, os.Getenv("LMSTUDIO_API_KEY"), 120*time.Second)
	ctx := context.Background()

	t.Run("Chat", func(t *testing.T) {
		resp, err := c.Chat(ctx, Request{
			Model:       model,
			Messages:    []Message{{Role: "user", Content: "Say exactly: pong"}},
			Temperature: 0,
			MaxTokens:   2048,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("chat reply: %q", resp.Content)
	})

	t.Run("Tool", func(t *testing.T) {
		var gotArgs string
		resp, err := RunTools(ctx, c, Request{
			Model: model,
			Messages: []Message{
				{Role: "system", Content: "Use the echo tool."},
				{Role: "user", Content: "Call the tool with the number 42."},
			},
			Tools:       []Tool{{Name: "echo", Description: "echo a number", Parameters: json.RawMessage(`{"type":"object","properties":{"n":{"type":"number"}}}`)}},
			Temperature: 0,
			MaxTokens:   2048,
		}, map[string]ToolFunc{
			"echo": func(_ context.Context, arguments string) (string, error) {
				gotArgs = arguments
				t.Logf("tool echo called with %s", arguments)
				return "got " + arguments, nil
			},
		}, 3)
		if err != nil {
			t.Fatal(err)
		}
		var args struct {
			N float64 `json:"n"`
		}
		if err := json.Unmarshal([]byte(gotArgs), &args); err != nil {
			t.Fatalf("tool args %q do not parse: %v", gotArgs, err)
		}
		if args.N != 42 {
			t.Errorf("tool called with n = %v, want 42", args.N)
		}
		if resp.Content == "" {
			t.Error("final Content is empty")
		}
		t.Logf("final: %q", resp.Content)
	})

	t.Run("Schema", func(t *testing.T) {
		schema, _ := json.Marshal(map[string]any{
			"type":       "object",
			"properties": map[string]any{"answer": map[string]any{"type": "string"}},
		})
		resp, err := c.Chat(ctx, Request{
			Model:       model,
			Messages:    []Message{{Role: "user", Content: "What is 2+2? Answer with a JSON object."}},
			Temperature: 0,
			MaxTokens:   2048,
			Schema:      schema,
			SchemaName:  "answer",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ExtractJSON(resp.Content); err != nil {
			t.Errorf("result %q is not JSON: %v", resp.Content, err)
		}
		t.Logf("schema result: %q", resp.Content)
	})
}

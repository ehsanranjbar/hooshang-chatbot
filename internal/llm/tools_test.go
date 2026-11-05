package llm

import (
	"context"
	"testing"
)

func TestRunToolsTwoRoundsThenFinal(t *testing.T) {
	fake := &Fake{}
	fake.Queue(Response{ToolCalls: []ToolCall{{ID: "c1", Name: "alpha", Arguments: `{"x":1}`}}})
	fake.Queue(Response{ToolCalls: []ToolCall{{ID: "c2", Name: "beta", Arguments: `{"y":2}`}}})
	fake.Queue(Response{Content: "done", FinishReason: "stop"})

	results := []string{}
	handlers := map[string]ToolFunc{
		"alpha": func(_ context.Context, args string) (string, error) {
			results = append(results, "alpha:"+args)
			return "ok-alpha", nil
		},
		"beta": func(_ context.Context, args string) (string, error) {
			results = append(results, "beta:"+args)
			return "ok-beta", nil
		},
	}

	resp, err := RunTools(context.Background(), fake, Request{Model: "m", Messages: []Message{{Role: "user", Content: "q"}}}, handlers, 5)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "done" {
		t.Errorf("Content = %q, want done", resp.Content)
	}
	if len(fake.Calls) != 3 {
		t.Fatalf("Chat calls = %d, want 3 (2 tool rounds + final)", len(fake.Calls))
	}
	if len(results) != 2 || results[0] != "alpha:{\"x\":1}" || results[1] != "beta:{\"y\":2}" {
		t.Errorf("tool results = %v", results)
	}
	// The last call must carry the assistant+tool transcript.
	last := fake.Calls[len(fake.Calls)-1].Messages
	want := []string{"user", "assistant", "tool", "assistant", "tool"}
	if len(last) != len(want) {
		t.Fatalf("final message roles = %v, want %v", roles(last), want)
	}
	for i, r := range want {
		if last[i].Role != r {
			t.Errorf("final message %d role = %q, want %q", i, last[i].Role, r)
		}
	}
	if last[2].ToolCallID != "c1" || last[2].Content != "ok-alpha" {
		t.Errorf("tool message = %+v", last[2])
	}
}

func TestRunToolsMaxRoundsExhaustedMakesOneFinalCallWithoutTools(t *testing.T) {
	fake := &Fake{}
	// The model always asks for a tool.
	fake.Queue(Response{ToolCalls: []ToolCall{{ID: "c1", Name: "alpha", Arguments: `{}`}}})
	fake.Queue(Response{ToolCalls: []ToolCall{{ID: "c2", Name: "alpha", Arguments: `{}`}}})
	fake.Queue(Response{Content: "giving up", FinishReason: "stop"})

	handlers := map[string]ToolFunc{
		"alpha": func(_ context.Context, args string) (string, error) { return "ok", nil },
	}

	resp, err := RunTools(context.Background(), fake, Request{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "q"}},
		Tools:    []Tool{{Name: "alpha", Description: "d"}},
	}, handlers, 2)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "giving up" {
		t.Errorf("Content = %q, want giving up", resp.Content)
	}
	if len(fake.Calls) != 3 {
		t.Fatalf("Chat calls = %d, want 3 (2 tool rounds + 1 final)", len(fake.Calls))
	}
	if len(fake.Calls[2].Tools) != 0 {
		t.Errorf("final call still has %d tools, want 0", len(fake.Calls[2].Tools))
	}
}

func TestRunToolsUnknownToolGivesErrorResult(t *testing.T) {
	fake := &Fake{}
	fake.Queue(Response{ToolCalls: []ToolCall{{ID: "c1", Name: "nope", Arguments: `{}`}}})
	fake.Queue(Response{Content: "final", FinishReason: "stop"})

	// A handlers map that knows no tools reports the call as an error.
	resp, err := RunTools(context.Background(), fake, Request{Model: "m", Messages: []Message{{Role: "user", Content: "q"}}}, map[string]ToolFunc{}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "final" {
		t.Errorf("Content = %q", resp.Content)
	}
	last := fake.Calls[len(fake.Calls)-1].Messages
	tool := last[len(last)-1]
	if tool.Role != "tool" || tool.Content != "error: unknown tool nope" {
		t.Errorf("tool result = %+v, want error: unknown tool nope", tool)
	}
}

func TestRunToolsFailedToolGivesErrorResult(t *testing.T) {
	fake := &Fake{}
	fake.Queue(Response{ToolCalls: []ToolCall{{ID: "c1", Name: "alpha", Arguments: `{}`}}})
	fake.Queue(Response{Content: "final", FinishReason: "stop"})

	handlers := map[string]ToolFunc{
		"alpha": func(_ context.Context, args string) (string, error) {
			return "partial", context.DeadlineExceeded
		},
	}

	if _, err := RunTools(context.Background(), fake, Request{Model: "m", Messages: []Message{{Role: "user", Content: "q"}}}, handlers, 5); err != nil {
		t.Fatal(err)
	}
	last := fake.Calls[len(fake.Calls)-1].Messages
	if got := last[len(last)-1].Content; got != "error: context deadline exceeded" {
		t.Errorf("tool result = %q", got)
	}
}

func roles(ms []Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Role
	}
	return out
}

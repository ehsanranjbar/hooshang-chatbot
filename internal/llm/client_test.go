package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient returns an HTTPClient pointed at the test server.
func newTestClient(ts *httptest.Server) *HTTPClient {
	return New(ts.URL, "key", time.Second)
}

// capture returns a handler that records every request body and replies with
// the given body and status.
func capture(t *testing.T, status int, body string) (func(w http.ResponseWriter, r *http.Request), *[]byte) {
	t.Helper()
	var last []byte
	h := func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		last = data
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	return h, &last
}

func TestChatRequestShape(t *testing.T) {
	h, last := capture(t, 200, `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`)
	ts := httptest.NewServer(http.HandlerFunc(h))
	defer ts.Close()

	req := Request{
		Model:       "m",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		Tools:       []Tool{{Name: "t", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Temperature: 0.7,
		MaxTokens:   2048,
	}
	if _, err := newTestClient(ts).Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(*last, &wire); err != nil {
		t.Fatalf("request is not JSON: %v", err)
	}
	if wire["model"] != "m" || wire["temperature"] != 0.7 || wire["max_tokens"] != float64(2048) {
		t.Errorf("scalar fields: %v", wire)
	}
	tools, _ := wire["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v, want 1", wire["tools"])
	}
	fn, _ := tools[0].(map[string]any)
	if fn["type"] != "function" || fn["function"].(map[string]any)["name"] != "t" {
		t.Errorf("tool shape: %v", fn)
	}
}

func TestChatRequestSchemaShape(t *testing.T) {
	h, last := capture(t, 200, `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`)
	ts := httptest.NewServer(http.HandlerFunc(h))
	defer ts.Close()

	req := Request{
		Model:       "m",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		Temperature: 0.7,
		MaxTokens:   2048,
		Schema:      json.RawMessage(`{"type":"object","properties":{"a":{}}}`),
		SchemaName:  "out",
	}
	if _, err := newTestClient(ts).Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(*last, &wire); err != nil {
		t.Fatalf("request is not JSON: %v", err)
	}
	if _, hasTools := wire["tools"]; hasTools {
		t.Errorf("unexpected tools in schema request: %v", wire["tools"])
	}
	rf, _ := wire["response_format"].(map[string]any)
	if rf == nil || rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v", wire["response_format"])
	}
	js, _ := rf["json_schema"].(map[string]any)
	if js["name"] != "out" {
		t.Errorf("json_schema.name = %v", js["name"])
	}
	if _, hasStrict := js["strict"]; hasStrict {
		t.Error("response_format must not contain strict")
	}
	if strings.Contains(string(*last), `"strict"`) {
		t.Error("request body contains strict")
	}
}

func TestChatParsesToolCalls(t *testing.T) {
	body := `{"choices":[{"message":{"content":"","tool_calls":[
		{"id":"c1","type":"function","function":{"name":"alpha","arguments":"{\"x\":1}"}},
		{"id":"c2","type":"function","function":{"name":"beta","arguments":"{\"y\":2}"}}
	]},"finish_reason":"tool_calls"}]}`
	h, _ := capture(t, 200, body)
	ts := httptest.NewServer(http.HandlerFunc(h))
	defer ts.Close()

	resp, err := newTestClient(ts).Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "q"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("ToolCalls = %v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].ID != "c1" || resp.ToolCalls[0].Name != "alpha" || resp.ToolCalls[0].Arguments != `{"x":1}` {
		t.Errorf("ToolCalls[0] = %+v", resp.ToolCalls[0])
	}
	if resp.ToolCalls[1].Name != "beta" {
		t.Errorf("ToolCalls[1] = %+v", resp.ToolCalls[1])
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
}

func TestChatRetryOn500(t *testing.T) {
	var n int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("boom"))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer ts.Close()

	resp, err := newTestClient(ts).Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "q"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q", resp.Content)
	}
	if n != 2 {
		t.Errorf("attempts = %d, want 2 (one retry on 500)", n)
	}
}

func TestChatRejectsSchemaWithTools(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server must not be called")
	}))
	defer ts.Close()

	req := Request{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "q"}},
		Tools:    []Tool{{Name: "t"}},
		Schema:   json.RawMessage(`{"type":"object"}`),
	}
	if _, err := newTestClient(ts).Chat(context.Background(), req); err == nil {
		t.Fatal("want error when Schema and Tools are both set")
	}
}

func TestChatTruncatedReasoningIsError(t *testing.T) {
	h, _ := capture(t, 200, `{"choices":[{"message":{"content":"","tool_calls":null},"finish_reason":"length"}]}`)
	ts := httptest.NewServer(http.HandlerFunc(h))
	defer ts.Close()

	_, err := newTestClient(ts).Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "q"}}})
	if err == nil || !strings.Contains(err.Error(), "output truncated") {
		t.Fatalf("err = %v, want output truncated", err)
	}
}

func TestChatNoRetryOn400(t *testing.T) {
	var n int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	}))
	defer ts.Close()

	if _, err := newTestClient(ts).Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "q"}}}); err == nil {
		t.Fatal("want error on 400")
	}
	if n != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 400)", n)
	}
}

func TestMessageWithImageMarshals(t *testing.T) {
	b, err := json.Marshal(Message{Role: "user", Content: "describe", Images: []Image{{MIME: "image/png", Data: []byte{1, 2, 3}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AQID"}}]}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
	plain, _ := json.Marshal(Message{Role: "user", Content: "hi"})
	if string(plain) != `{"role":"user","content":"hi"}` {
		t.Fatalf("plain = %s", plain)
	}
}

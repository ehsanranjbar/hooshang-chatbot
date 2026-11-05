package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
)

// Message is one chat message in the OpenAI-compatible wire shape.
type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content"`
	Images     []Image    `json:"-"` // sent with Content as content parts (vision models)
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Image is an image attached to a user message.
type Image struct {
	MIME string // image/png, image/jpeg
	Data []byte
}

// MarshalJSON writes a message with images as OpenAI content parts:
// [{type:text}, {type:image_url, image_url:{url:"data:…;base64,…"}}].
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	if len(m.Images) == 0 {
		return json.Marshal(plain(m))
	}
	type part struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url,omitempty"`
	}
	parts := []part{{Type: "text", Text: m.Content}}
	for _, img := range m.Images {
		p := part{Type: "image_url", ImageURL: &struct {
			URL string `json:"url"`
		}{URL: "data:" + img.MIME + ";base64," + base64.StdEncoding.EncodeToString(img.Data)}}
		parts = append(parts, p)
	}
	return json.Marshal(struct {
		Role    string `json:"role"`
		Content []part `json:"content"`
	}{m.Role, parts})
}

// ToolCall is a tool call requested by the model.
type ToolCall struct {
	ID, Name, Arguments string
}

// toolCallWire is the OpenAI wire shape of a tool call.
type toolCallWire struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// MarshalJSON renders the wire shape {id, type:"function", function:{name, arguments}}.
func (t ToolCall) MarshalJSON() ([]byte, error) {
	w := toolCallWire{ID: t.ID, Type: "function"}
	w.Function.Name = t.Name
	w.Function.Arguments = t.Arguments
	return json.Marshal(w)
}

// UnmarshalJSON reads the wire shape.
func (t *ToolCall) UnmarshalJSON(b []byte) error {
	var w toolCallWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	t.ID, t.Name, t.Arguments = w.ID, w.Function.Name, w.Function.Arguments
	return nil
}

// Tool is a function the model may call. Parameters is a JSON Schema object.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// toolWire is the OpenAI wire shape of a tool: {type:"function", function:{...}}.
type toolWire struct {
	Type     string `json:"type"`
	Function Tool   `json:"function"`
}

// Request is one chat completion request.
type Request struct {
	Model       string
	Messages    []Message
	Tools       []Tool
	Temperature float64
	MaxTokens   int
	Schema      json.RawMessage // non-nil → response_format json_schema (never "strict")
	SchemaName  string
}

// requestWire is the JSON sent to the API.
type requestWire struct {
	Model          string           `json:"model"`
	Messages       []Message        `json:"messages"`
	Tools          []toolWire       `json:"tools,omitempty"`
	Temperature    float64          `json:"temperature"`
	MaxTokens      int              `json:"max_tokens"`
	ResponseFormat *responseFormatW `json:"response_format,omitempty"`
}

// responseFormatW is the json_schema response_format shape.
// NOTE: no "strict" field — Gemma produces degenerate output with strict:true.
type responseFormatW struct {
	Type       string `json:"type"` // "json_schema"
	JSONSchema struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
	} `json:"json_schema"`
}

// Response is the useful part of a chat completion response.
// The model's reasoning_content is deliberately ignored.
type Response struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
}

// responseWire is the JSON returned by the API.
type responseWire struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// Client is the LLM interface every other package depends on.
type Client interface {
	Chat(ctx context.Context, req Request) (Response, error)
}

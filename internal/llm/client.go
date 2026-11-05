// Package llm is a small OpenAI-compatible chat client, written for LM Studio
// and OpenRouter. It has no dependencies beyond the standard library.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// retryDelay is the wait between the single retry of a failed request.
const retryDelay = 2 * time.Second

// HTTPClient is a Client backed by an OpenAI-compatible HTTP API.
type HTTPClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New returns an HTTPClient for the given API base URL (e.g.
// http://localhost:1234/v1). The timeout bounds each request.
func New(baseURL, apiKey string, timeout time.Duration) *HTTPClient {
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: timeout},
	}
}

// Chat sends one chat completion request. It retries once, after 2s, on a
// network error or HTTP 5xx/429; it does not retry other 4xx errors.
// The returned Content passes through Clean.
func (c *HTTPClient) Chat(ctx context.Context, req Request) (Response, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Response{}, ctx.Err()
			case <-time.After(retryDelay):
			}
		}
		resp, retryable, err := c.do(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retryable {
			return Response{}, err
		}
	}
	return Response{}, lastErr
}

// do performs one HTTP attempt. It reports whether the failure is retryable.
func (c *HTTPClient) do(ctx context.Context, req Request) (Response, bool, error) {
	if req.Schema != nil && len(req.Tools) > 0 {
		return Response{}, false, fmt.Errorf("llm: response_format (Schema) and Tools cannot both be set")
	}
	wire := requestWire{
		Model:          req.Model,
		Messages:       req.Messages,
		Temperature:    req.Temperature,
		MaxTokens:      req.MaxTokens,
		ResponseFormat: buildResponseFormat(req.Schema, req.SchemaName),
	}
	for _, t := range req.Tools {
		wire.Tools = append(wire.Tools, toolWire{Type: "function", Function: t})
	}
	data, err := json.Marshal(wire)
	if err != nil {
		return Response{}, false, fmt.Errorf("llm: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return Response{}, false, fmt.Errorf("llm: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Response{}, true, fmt.Errorf("llm: request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Response{}, true, fmt.Errorf("llm: read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		retryable := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return Response{}, retryable, fmt.Errorf("llm: HTTP %d: %s", resp.StatusCode, first300(body))
	}
	var rw responseWire
	if err := json.Unmarshal(body, &rw); err != nil {
		return Response{}, false, fmt.Errorf("llm: decode response: %v: %s", err, first300(body))
	}
	if len(rw.Choices) == 0 {
		return Response{}, false, fmt.Errorf("llm: empty choices: %s", first300(body))
	}
	ch := rw.Choices[0]
	out := Response{
		Content:      Clean(ch.Message.Content),
		ToolCalls:    ch.Message.ToolCalls,
		FinishReason: ch.FinishReason,
	}
	if out.FinishReason == "length" && out.Content == "" && len(out.ToolCalls) == 0 {
		return Response{}, false, fmt.Errorf("llm: output truncated (max_tokens too low for reasoning)")
	}
	return out, false, nil
}

// buildResponseFormat builds the json_schema response_format when a schema is
// set. It never includes "strict".
func buildResponseFormat(schema json.RawMessage, name string) *responseFormatW {
	if schema == nil {
		return nil
	}
	rf := &responseFormatW{Type: "json_schema"}
	rf.JSONSchema.Name = name
	rf.JSONSchema.Schema = schema
	return rf
}

// first300 returns at most the first 300 bytes of b, for error messages.
func first300(b []byte) string {
	if len(b) > 300 {
		return string(b[:300])
	}
	return string(b)
}

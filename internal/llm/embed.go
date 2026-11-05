package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Embedder turns texts into vectors (an OpenAI-compatible /embeddings call).
type Embedder interface {
	Embed(ctx context.Context, model string, texts []string) ([][]float32, error)
}

// Embed implements Embedder: one POST /embeddings with all the texts, the
// vectors returned in input order.
func (c *HTTPClient) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{model, texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("embeddings: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: HTTP %d: %s", resp.StatusCode, first300(raw))
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("embeddings: decode: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d texts", len(out.Data), len(texts))
	}
	sort.Slice(out.Data, func(i, j int) bool { return out.Data[i].Index < out.Data[j].Index })
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}

// Embed implements Embedder for the Fake: a deterministic bag-of-words vector
// (each word hashed into 256 dimensions), so texts that share words are close
// and texts that share none are not. Every call is counted in EmbedCalls.
func (f *Fake) Embed(_ context.Context, _ string, texts []string) ([][]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.EmbedCalls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 256)
		for _, w := range strings.Fields(strings.ToLower(t)) {
			w = strings.Trim(w, "،,.!?؟:«»\"'")
			if w == "" {
				continue
			}
			h := fnv.New32a()
			h.Write([]byte(w))
			v[h.Sum32()%256] += 1
		}
		out[i] = v
	}
	return out, nil
}

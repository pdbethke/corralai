// SPDX-License-Identifier: Elastic-2.0

// Package embed turns text into vectors via a configurable, OpenAI-compatible
// /v1/embeddings endpoint — shared by the reference (RAG) and memory (hive-mind)
// engines so the whole swarm embeds into ONE vector space. nil Client => disabled
// (no hard dependency); callers fall back to keyword search.
package embed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	url   string
	model string
	key   string
	httpc *http.Client
}

func New() *Client {
	return NewFor(os.Getenv("CORRALAI_EMBED_URL"), os.Getenv("CORRALAI_EMBED_MODEL"), os.Getenv("CORRALAI_EMBED_KEY"))
}

func NewFor(url, model, key string) *Client {
	if url == "" {
		return nil
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	return &Client{url: url, model: model, key: key, httpc: &http.Client{Timeout: 60 * time.Second}}
}

// VecLiteral renders a float vector as a DuckDB list literal: [0.1,0.2,...].
// Shared by memory and reference stores so both embed into the same format.
func VecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// Model returns the model name configured for this client.
func (c *Client) Model() string { return c.model }

// Embed returns one vector per input text. Cosine search is scale-invariant, so
// vectors are stored as returned (no normalization needed).
func (c *Client) Embed(texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(map[string]any{"model": c.model, "input": texts})
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return nil, fmt.Errorf("embeddings endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode embeddings: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("expected %d embeddings, got %d", len(texts), len(out.Data))
	}
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		v := make([]float32, len(d.Embedding))
		for j, x := range d.Embedding {
			v[j] = float32(x)
		}
		vecs[i] = v
	}
	return vecs, nil
}

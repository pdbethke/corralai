// SPDX-License-Identifier: Elastic-2.0

// Package modelcatalog asks an operator-declared model source which models it
// serves, so a bare local model name can resolve to the daemon that has it.
package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	listTimeout = 15 * time.Second
	maxBody     = 8 << 20
)

// ListOllama returns the model names an ollama daemon at base serves, from
// <base>/api/tags. base has no default: an empty base is refused, because
// silently listing some other daemon would route a seat to the wrong machine.
func ListOllama(ctx context.Context, hc *http.Client, base string) ([]string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, errors.New("modelcatalog: ollama base is empty; pass the daemon's URL explicitly")
	}
	if hc == nil {
		return nil, errors.New("modelcatalog: nil http client")
	}
	var r struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := getJSON(ctx, hc, base+"/api/tags", &r); err != nil {
		return nil, fmt.Errorf("could not list models at %s: %w", base, err)
	}
	if r.Models == nil {
		return nil, fmt.Errorf("ollama at %s: response has no \"models\" list", base)
	}
	names := make([]string, 0, len(r.Models))
	for _, m := range r.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// getJSON performs one GET under a per-request timeout and decodes a 2xx body
// into out. A non-2xx status is an error naming the code and a body excerpt.
func getJSON(ctx context.Context, hc *http.Client, rawURL string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("reading body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		excerpt := strings.Join(strings.Fields(string(body)), " ")
		if len(excerpt) > 200 {
			excerpt = excerpt[:200]
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, excerpt)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

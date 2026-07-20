// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pdbethke/corralai/internal/brainclient"
	"github.com/pdbethke/corralai/internal/criticscore"
)

// criticScoreLister is the read surface `corral criticscore list` needs:
// the findings still awaiting human adjudication. Narrowed to one method so
// tests can inject a fake without a running brain — same shape as
// scorecardReader in scorecard.go.
type criticScoreLister interface {
	ListPending(ctx context.Context) ([]criticscore.Finding, error)
}

// criticScoreAdmin is the write/detail surface `show`, `confirm`, and
// `refute` need. Both Get and Adjudicate are ADMIN-gated MCP tools on the
// brain (internal/brain/criticscoretools.go) — they must carry the caller's
// bearer identity for the isHumanAdmin check and the audit trail, so
// (unlike ListPending's plain, unauthenticated-past-the-mux REST read) they
// go over MCP, not a REST endpoint.
type criticScoreAdmin interface {
	Get(ctx context.Context, id string) (criticscore.Finding, error)
	Adjudicate(ctx context.Context, id, verdict string) (string, error)
}

// httpCriticScoreLister reads the pending list over the wire from a running
// brain's GET /api/criticscore (see internal/ui.Server.criticScorePending),
// the same single-process reasoning as httpScorecardReader in scorecard.go:
// the criticscore DuckDB file is held read-write by corral.service, so a
// second process cannot open it concurrently.
type httpCriticScoreLister struct {
	brainURL string
	client   *http.Client
}

func newHTTPCriticScoreLister(brainURL, token string) *httpCriticScoreLister {
	hc := brainclient.AuthedHTTPClient(token)
	hc.Timeout = 15 * time.Second
	return &httpCriticScoreLister{brainURL: brainURL, client: hc}
}

func (r *httpCriticScoreLister) ListPending(ctx context.Context) ([]criticscore.Finding, error) {
	url := strings.TrimRight(r.brainURL, "/") + "/api/criticscore"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("criticscore list: build request: %w", err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("criticscore list: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("criticscore list: GET %s: unexpected status %s", url, resp.Status)
	}
	var body struct {
		Findings []criticscore.Finding `json:"findings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("criticscore list: decode response: %w", err)
	}
	return body.Findings, nil
}

// mcpCriticScoreAdmin backs show/confirm/refute with real MCP calls,
// dialing the brain fresh per call with a token from the keystore — mirrors
// mcpAdvClient in certify_adversarial.go.
type mcpCriticScoreAdmin struct{ brainURL string }

func (a mcpCriticScoreAdmin) call(ctx context.Context, tool string, args map[string]any) (string, error) {
	token, err := brainToken()
	if err != nil {
		return "", fmt.Errorf("resolve brain token: %w", err)
	}
	cl, err := brainclient.Dial(ctx, a.brainURL, token)
	if err != nil {
		return "", err
	}
	defer func() { _ = cl.Close() }()
	res, err := cl.CallTool(ctx, tool, args)
	if err != nil {
		return "", err
	}
	text := brainclient.FirstText(res)
	if res.IsError {
		msg := text
		if msg == "" {
			msg = tool + " reported an error"
		}
		return "", fmt.Errorf("%s", msg)
	}
	return text, nil
}

func (a mcpCriticScoreAdmin) Get(ctx context.Context, id string) (criticscore.Finding, error) {
	text, err := a.call(ctx, "get_critic_finding", map[string]any{"id": id})
	if err != nil {
		return criticscore.Finding{}, err
	}
	var f criticscore.Finding
	if err := json.Unmarshal([]byte(text), &f); err != nil {
		return criticscore.Finding{}, fmt.Errorf("decoding get_critic_finding response: %w", err)
	}
	return f, nil
}

func (a mcpCriticScoreAdmin) Adjudicate(ctx context.Context, id, verdict string) (string, error) {
	text, err := a.call(ctx, "adjudicate_critic_finding", map[string]any{"id": id, "verdict": verdict})
	if err != nil {
		return "", err
	}
	var out struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return "", fmt.Errorf("decoding adjudicate_critic_finding response: %w", err)
	}
	if !out.OK {
		return "", fmt.Errorf("%s", out.Message)
	}
	return out.Message, nil
}

// runCriticScore implements `corral criticscore list|show <id>|confirm
// <id>|refute <id>` — the human gate over the adversarial pool's
// execution-checked test-critic findings (internal/criticscore). list is a
// plain read; show/confirm/refute go through the ADMIN-gated MCP tools
// (internal/brain/criticscoretools.go), so a caller without admin rights
// gets that tool's own rejection surfaced as an error here.
func runCriticScore(args []string, lister criticScoreLister, admin criticScoreAdmin, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: corral criticscore list|show <id>|confirm <id>|refute <id>")
		return 2
	}
	ctx := context.Background()
	switch args[0] {
	case "list":
		findings, err := lister.ListPending(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "corral criticscore list:", err)
			return 1
		}
		if len(findings) == 0 {
			fmt.Fprintln(stdout, "no pending critic findings")
			return 0
		}
		tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tMODEL\tTARGET TEST\tSCOPE\tSEVERITY\t")
		for _, f := range findings {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t\n", f.ID, f.Model, f.TargetTest, f.Scope, f.Severity)
		}
		tw.Flush()
		return 0

	case "show":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: corral criticscore show <id>")
			return 2
		}
		f, err := admin.Get(ctx, args[1])
		if err != nil {
			fmt.Fprintln(stderr, "corral criticscore show:", err)
			return 1
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(f)
		return 0

	case "confirm", "refute":
		if len(args) < 2 {
			fmt.Fprintf(stderr, "usage: corral criticscore %s <id>\n", args[0])
			return 2
		}
		verdict := "confirmed"
		if args[0] == "refute" {
			verdict = "refuted"
		}
		msg, err := admin.Adjudicate(ctx, args[1], verdict)
		if err != nil {
			fmt.Fprintln(stderr, "corral criticscore "+args[0]+":", err)
			return 1
		}
		fmt.Fprintln(stdout, msg)
		return 0

	default:
		fmt.Fprintln(stderr, "usage: corral criticscore list|show <id>|confirm <id>|refute <id>")
		return 2
	}
}

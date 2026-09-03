// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os/user"
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
	Adjudicate(ctx context.Context, id, verdict, rationale string) (string, error)
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

func (a mcpCriticScoreAdmin) Adjudicate(ctx context.Context, id, verdict, rationale string) (string, error) {
	args := map[string]any{"id": id, "verdict": verdict}
	// Omitted rather than sent empty: the brain's tool treats rationale as
	// optional, and an explicit "" would overwrite a rationale a previous
	// adjudication recorded.
	if rationale != "" {
		args["rationale"] = rationale
	}
	text, err := a.call(ctx, "adjudicate_critic_finding", args)
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
	// HELP IS ANSWERED HERE, before the dispatch, and that is load-bearing: the
	// caller's wantsHelp guard scans EVERY argument, so `criticscore list -h`
	// takes the help path with nil collaborators — and then this switch called
	// lister.ListPending on the nil and SEGFAULTED. The crash text was captured
	// verbatim into the generated CLI reference, which is how it was found.
	//
	// A command must be able to say what it does with no store, no arguments
	// and no collaborators wired.
	if len(args) == 0 || wantsHelp(args) {
		fmt.Fprintln(stderr, "usage: corral criticscore list|show <id>|confirm <id> [--why ...]|refute <id> [--why ...]")
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
			fmt.Fprintf(stderr, "usage: corral criticscore %s <id> [--why \"...\"]\n", args[0])
			return 2
		}
		verdict := "confirmed"
		if args[0] == "refute" {
			verdict = "refuted"
		}
		// --why records the BASIS for the verdict. Optional on purpose: a
		// required justification is how you get "ok" typed a hundred times.
		// But a corpus that stores only the verdict makes C-PREC a number
		// nobody can audit — you cannot tell a considered refutation from a
		// careless one.
		fs := flag.NewFlagSet("criticscore "+args[0], flag.ContinueOnError)
		fs.SetOutput(stderr)
		why := fs.String("why", "", "why this verdict was reached — the evidence you actually checked")
		if err := fs.Parse(args[2:]); err != nil {
			return 2
		}
		msg, err := admin.Adjudicate(ctx, args[1], verdict, strings.TrimSpace(*why))
		if err != nil {
			fmt.Fprintln(stderr, "corral criticscore "+args[0]+":", err)
			return 1
		}
		fmt.Fprintln(stdout, msg)
		return 0

	default:
		fmt.Fprintln(stderr, "usage: corral criticscore list|show <id>|confirm <id> [--why ...]|refute <id> [--why ...]")
		return 2
	}
}

// localCriticScore adapts the on-disk criticscore store to the lister/admin
// interfaces above, so `corral criticscore` works with no brain running.
//
// It exists because the corpus was unreachable for exactly the people who most
// need it. `certify --local` is what the quickstart, the README and every
// external user runs, and criticscore refused without CORRAL_BRAIN — so a
// local user got critic findings printed once to a terminal and no way to
// record that they had checked one. `corral scorecard`'s C-PREC column, whose
// whole purpose is the critic's execution-checked precision from those human
// verdicts, therefore read "—" forever for anyone without a daemon.
//
// The brain's admin gate is NOT weakened by this. That gate exists to attribute
// an adjudication to a verified human principal on a shared, multi-user brain;
// a local DuckDB file under the operator's own $HOME has exactly one principal,
// who already has write access to the file. Adjudications made here are
// attributed to the local OS user for the same reason the brain attributes to a
// bearer identity: so the row says who decided.
type localCriticScore struct{ store *criticscore.Store }

func (l localCriticScore) ListPending(ctx context.Context) ([]criticscore.Finding, error) {
	return l.store.ListPending(ctx)
}

func (l localCriticScore) Get(ctx context.Context, id string) (criticscore.Finding, error) {
	f, ok, err := l.store.Get(ctx, id)
	if err != nil {
		return criticscore.Finding{}, err
	}
	if !ok {
		return criticscore.Finding{}, fmt.Errorf("no critic finding %q — run `corral criticscore list` to see pending ids", id)
	}
	return f, nil
}

func (l localCriticScore) Adjudicate(ctx context.Context, id, verdict, rationale string) (string, error) {
	by := localAdjudicator()
	ok, err := l.store.Adjudicate(ctx, id, verdict, by, rationale)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no critic finding %q — nothing was recorded", id)
	}
	return fmt.Sprintf("%s recorded as %s by %s", id, verdict, by), nil
}

// localAdjudicator names who made a local adjudication. An unknown user is
// recorded as "local" rather than left empty: a row that cannot say who
// decided is worse than one that says "someone at this machine".
func localAdjudicator() string {
	if u, err := user.Current(); err == nil && strings.TrimSpace(u.Username) != "" {
		return "local:" + u.Username
	}
	return "local"
}

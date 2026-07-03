// SPDX-License-Identifier: Elastic-2.0

// corral-top — htop for the herd: a zero-dependency terminal viewport over a
// brain's /api/state. Missions, tasks, the herd, findings, and the live console,
// redrawn in place every couple of seconds.
//
//	CORRAL_BRAIN=http://localhost:9019 corral-top
//	corral-top --brain https://brain.example --token <observer-token>
//
// A read-only observer token is all it needs — this is exactly the surface
// mint_observer exists for. When stdout is not a TTY it prints ONE frame and
// exits (pipe it, cron it, test it).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"
)

type state struct {
	ServerNow    float64 `json:"server_now"`
	ActiveAgents []struct {
		Name   string `json:"name"`
		Role   string `json:"role"`
		Status string `json:"status"`
		Task   string `json:"task"`
	} `json:"active_agents"`
	Missions []struct {
		ID        int64  `json:"id"`
		Directive string `json:"directive"`
		Status    string `json:"status"`
		Sprint    int    `json:"sprint"`
	} `json:"missions"`
	Tasks []struct {
		Key       string `json:"key"`
		Title     string `json:"title"`
		Role      string `json:"role"`
		Status    string `json:"status"`
		ClaimedBy string `json:"claimed_by"`
	} `json:"tasks"`
	Findings []struct {
		Severity string `json:"severity"`
		Type     string `json:"type"`
		Status   string `json:"status"`
	} `json:"findings"`
	LiveClaims []struct {
		Agent string `json:"agent"`
		Path  string `json:"path"`
	} `json:"live_claims"`
	RecentActivity []struct {
		Agent  string `json:"agent"`
		Tool   string `json:"tool"`
		Detail string `json:"detail"`
	} `json:"recent_activity"`
}

const (
	clr   = "\033[2J\033[H"
	dim   = "\033[2m"
	bold  = "\033[1m"
	red   = "\033[31m"
	grn   = "\033[32m"
	yel   = "\033[33m"
	cyn   = "\033[36m"
	off   = "\033[0m"
	width = 100
)

func bar(done, total, w int) string {
	if total == 0 {
		return strings.Repeat("░", w)
	}
	f := done * w / total
	return grn + strings.Repeat("█", f) + off + dim + strings.Repeat("░", w-f) + off
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// render turns one state snapshot into one frame. Pure — the tests live here.
func render(s *state, brain string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%scorral-top%s — %s   %s%s%s\n", bold, off, brain, dim, time.Now().Format("15:04:05"), off)
	b.WriteString(strings.Repeat("─", width) + "\n")

	// Missions + task rollup.
	counts := map[string]int{}
	for _, t := range s.Tasks {
		counts[t.Status]++
	}
	total := len(s.Tasks)
	done := counts["done"]
	for _, m := range s.Missions {
		statColor := yel
		switch m.Status {
		case "done":
			statColor = grn
		case "awaiting_review":
			statColor = cyn
		case "failed":
			statColor = red
		}
		fmt.Fprintf(&b, "%smission #%d%s [%s%s%s] sprint %d  %s\n", bold, m.ID, off, statColor, m.Status, off, m.Sprint, trunc(m.Directive, 60))
	}
	if total > 0 {
		fmt.Fprintf(&b, "tasks %d/%d %s  %s%v%s\n", done, total, bar(done, total, 40), dim, orderedCounts(counts), off)
	}

	// The herd.
	fmt.Fprintf(&b, "\n%sthe herd%s (%d)\n", bold, off, len(s.ActiveAgents))
	agents := s.ActiveAgents
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	for _, a := range agents {
		st := a.Status
		c := dim
		if st == "" {
			st = "—"
		}
		if st == "working" {
			c = grn
		}
		claims := 0
		for _, cl := range s.LiveClaims {
			if cl.Agent == a.Name {
				claims++
			}
		}
		claimNote := ""
		if claims > 0 {
			claimNote = fmt.Sprintf("  %s⚿ %d claim(s)%s", yel, claims, off)
		}
		fmt.Fprintf(&b, "  %-10s %-11s %s%-9s%s%s\n", trunc(a.Name, 10), trunc(a.Role, 11), c, st, off, claimNote)
	}

	// Findings.
	open, sev := 0, map[string]int{}
	for _, f := range s.Findings {
		if f.Status == "open" {
			open++
			sev[f.Severity]++
		}
	}
	fmt.Fprintf(&b, "\n%sfindings%s %d open", bold, off, open)
	if open > 0 {
		fmt.Fprintf(&b, "  %s(crit %d · high %d · med %d · low %d)%s", red, sev["critical"], sev["high"], sev["medium"], sev["low"], off)
	}
	b.WriteString("\n")

	// Console tail.
	fmt.Fprintf(&b, "\n%sconsole%s\n", bold, off)
	acts := s.RecentActivity
	if len(acts) > 8 {
		acts = acts[len(acts)-8:]
	}
	for _, a := range acts {
		fmt.Fprintf(&b, "  %s%-10s%s %s%-16s%s %s\n", cyn, trunc(a.Agent, 10), off, dim, trunc(a.Tool, 16), off, trunc(a.Detail, 56))
	}
	b.WriteString(strings.Repeat("─", width) + "\n" + dim + "q or Ctrl-C to quit" + off + "\n")
	return b.String()
}

// orderedCounts renders status counts in pipeline order, stable for tests.
func orderedCounts(c map[string]int) string {
	order := []string{"pending", "ready", "claimed", "done", "cancelled", "superseded"}
	parts := []string{}
	for _, k := range order {
		if c[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, c[k]))
		}
	}
	return strings.Join(parts, " · ")
}

func fetch(client *http.Client, brain, token string) (*state, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(brain, "/")+"/api/state", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brain returned %s", resp.Status)
	}
	var s state
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

func main() {
	brain := flag.String("brain", envOr("CORRAL_BRAIN", "http://localhost:9019"), "brain base URL")
	token := flag.String("token", os.Getenv("CORRAL_TOKEN"), "bearer token (a read-only observer token is enough)")
	interval := flag.Duration("interval", 2*time.Second, "refresh interval")
	flag.Parse()

	client := &http.Client{Timeout: 10 * time.Second}

	// Non-TTY: one frame, no escape codes beyond colors, exit.
	if fi, _ := os.Stdout.Stat(); fi != nil && fi.Mode()&os.ModeCharDevice == 0 {
		s, err := fetch(client, *brain, *token)
		if err != nil {
			fmt.Fprintln(os.Stderr, "corral-top:", err)
			os.Exit(1)
		}
		fmt.Print(render(s, *brain))
		return
	}

	// 'q' to quit — read stdin raw-ish without termios: a line-buffered q works
	// everywhere; Ctrl-C is the primary exit.
	go func() {
		buf := make([]byte, 1)
		for {
			if n, err := os.Stdin.Read(buf); err != nil || (n > 0 && (buf[0] == 'q' || buf[0] == 'Q')) {
				fmt.Print("\033[?25h") // cursor back on
				os.Exit(0)
			}
		}
	}()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt)
	go func() { <-sigc; fmt.Print("\033[?25h"); os.Exit(0) }()

	fmt.Print("\033[?25l") // hide cursor
	for {
		s, err := fetch(client, *brain, *token)
		if err != nil {
			fmt.Printf("%s%scorral-top%s — %s unreachable: %v (retrying)\n", clr, bold, off, *brain, err)
		} else {
			fmt.Print(clr + render(s, *brain))
		}
		time.Sleep(*interval)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

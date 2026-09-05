// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/coord"
)

// The broker verbs: the claim broker WITHOUT the daemon. A coding-agent
// session on one machine registers, claims the paths it is about to edit,
// heartbeats while it works, leaves a handoff note and releases — against
// the same internal/coord store the server uses, opened as a local file.
// No listener, no IdP: on one machine the OS user is the principal, and the
// record says who held what and when.
//
// This is the founder's own daily coordination shape (a Python skill that
// claims files and heartbeats, in use by the sessions writing this code),
// made a first-class verb of the binary it always belonged to. It is also
// the substrate the adversarial review loop's seats will claim work
// through: a review claim is a file claim with a different reason.
//
// Every verb takes --as <session>; unset, the session is "<user>@<host>",
// which is right for one interactive session per machine and wrong for
// two, so the sessions that matter pass their own.

// brokerDBPath is the ONE coordination store, the same file the daemon
// serves: $CORRALAI_DB, default ~/.claude/corralai_coord.sqlite3. A claim
// made from the shell is the claim the MCP claim_task sees, and vice versa
// — two doors, one record. A second file would have meant a running daemon
// and a CLI session on the same box disagreeing about who holds what.
func brokerDBPath() string {
	if p := strings.TrimSpace(os.Getenv("CORRALAI_DB")); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "corralai_coord.sqlite3")
}

func principal() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

func defaultSession() string {
	host, _ := os.Hostname()
	return principal() + "@" + host
}

func openBroker() (*coord.Store, error) {
	p := brokerDBPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	return coord.Open(p)
}

// runBroker dispatches the verbs; exit 2 on usage, 1 on a refused claim
// (so a shell `&&` stops before the edit), 0 otherwise.
func runBroker(verb string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral-wrangler "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	as := fs.String("as", "", "this session's name (default <user>@<host>)")
	task := fs.String("task", "", "what this session is doing — the handoff note other sessions read")
	reason := fs.String("reason", "", "claim: why these paths (shown to a session that collides)")
	ttl := fs.Duration("ttl", 2*time.Hour, "claim: how long the claim holds without a heartbeat")
	advisory := fs.Bool("advisory", false, "claim: record the claim without excluding others")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	name := strings.TrimSpace(*as)
	if name == "" {
		name = defaultSession()
	}
	st, err := openBroker()
	if err != nil {
		fmt.Fprintf(stderr, "corral-wrangler %s: opening the broker store at %s: %v\n", verb, brokerDBPath(), err)
		return 1
	}
	defer st.Close()
	_ = st.RecordPrincipal(name, principal())

	emit := func(v any, plain func()) {
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(v)
			return
		}
		plain()
	}

	switch verb {
	case "register", "heartbeat":
		// One verb in two spellings: a first heartbeat registers, a later
		// register refreshes. Either way the task note is updated when given.
		b, err := st.BootstrapSession(name, "cli", "", *task, name, "")
		if err != nil {
			fmt.Fprintf(stderr, "corral-wrangler %s: %v\n", verb, err)
			return 1
		}
		if *task == "" {
			_ = st.Heartbeat(name)
		}
		emit(b, func() {
			fmt.Fprintf(stdout, "%s: registered as %s (principal %s)\n", verb, name, principal())
			if len(b.ActivePeers) == 0 {
				fmt.Fprintln(stdout, "  no other active sessions")
			}
			for _, p := range b.ActivePeers {
				fmt.Fprintf(stdout, "  peer %s — %s (last active %s ago)\n", p.Name, orNone(p.Task), ago(p.LastActiveTS))
			}
			for _, c := range b.Contested {
				fmt.Fprintf(stdout, "  CONTESTED %s — held by %s (%s)\n", c.Path, c.HeldBy, c.Reason)
			}
		})
		return 0
	case "claim":
		paths := fs.Args()
		if len(paths) == 0 {
			fmt.Fprintln(stderr, "corral-wrangler claim: name at least one path")
			return 2
		}
		if _, err := st.BootstrapSession(name, "cli", "", *task, name, ""); err != nil {
			fmt.Fprintf(stderr, "corral-wrangler claim: %v\n", err)
			return 1
		}
		res, err := st.ClaimPaths(name, paths, ttl.Seconds(), !*advisory, *reason)
		if err != nil {
			fmt.Fprintf(stderr, "corral-wrangler claim: %v\n", err)
			return 1
		}
		emit(res, func() {
			for _, p := range res.Granted {
				fmt.Fprintf(stdout, "claimed %s (until %s)\n", p, time.Unix(int64(res.ExpiresTS), 0).Format(time.Kitchen))
			}
			for _, c := range res.Conflicts {
				fmt.Fprintf(stdout, "REFUSED %s — held by %s: %s\n", c.Path, c.HeldBy, orNone(c.Reason))
			}
		})
		if len(res.Conflicts) > 0 {
			return 1
		}
		return 0
	case "release":
		n, err := st.ReleaseClaims(name, fs.Args())
		if err != nil {
			fmt.Fprintf(stderr, "corral-wrangler release: %v\n", err)
			return 1
		}
		emit(map[string]any{"released": n}, func() { fmt.Fprintf(stdout, "released %d claim(s)\n", n) })
		return 0
	case "done":
		if err := st.MarkDone(name, *task, fs.Args()); err != nil {
			fmt.Fprintf(stderr, "corral-wrangler done: %v\n", err)
			return 1
		}
		n, _ := st.ReleaseClaims(name, nil)
		emit(map[string]any{"released": n, "summary": *task}, func() { fmt.Fprintf(stdout, "done: %s — released %d claim(s)\n", orNone(*task), n) })
		return 0
	case "who":
		target := name
		if rest := fs.Args(); len(rest) == 1 {
			target = rest[0]
		}
		a, claims, err := st.Whois(target)
		if err != nil {
			fmt.Fprintf(stderr, "corral-wrangler who: %v\n", err)
			return 1
		}
		if a == nil {
			fmt.Fprintf(stdout, "%s: not registered\n", target)
			return 1
		}
		emit(map[string]any{"agent": a, "claims": claims}, func() {
			fmt.Fprintf(stdout, "%s — %s (last active %s ago)\n", a.Name, orNone(a.Task), ago(a.LastActiveTS))
			for _, c := range claims {
				fmt.Fprintf(stdout, "  holds %s until %s%s\n", c.Path, time.Unix(int64(c.ExpiresTS), 0).Format(time.Kitchen), reasonSuffix(c.Reason))
			}
		})
		return 0
	case "list":
		s, err := st.CoordinationStatus(coord.PresenceWindow)
		if err != nil {
			fmt.Fprintf(stderr, "corral-wrangler list: %v\n", err)
			return 1
		}
		emit(s, func() {
			if len(s.ActiveAgents) == 0 {
				fmt.Fprintln(stdout, "no active sessions")
			}
			for _, a := range s.ActiveAgents {
				fmt.Fprintf(stdout, "%s — %s (last active %s ago)\n", a.Name, orNone(a.Task), ago(a.LastActiveTS))
			}
			for _, c := range s.LiveClaims {
				fmt.Fprintf(stdout, "  %s holds %s%s\n", c.Agent, c.Path, reasonSuffix(c.Reason))
			}
			for _, d := range s.RecentCompleted {
				fmt.Fprintf(stdout, "  done: %s — %s\n", d.AgentName, d.Summary)
			}
		})
		return 0
	}
	fmt.Fprintf(stderr, "corral-wrangler: unknown verb %q\n", verb)
	return 2
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(no note)"
	}
	return s
}

func reasonSuffix(r string) string {
	if strings.TrimSpace(r) == "" {
		return ""
	}
	return " — " + r
}

func ago(ts float64) string {
	if ts <= 0 {
		return "never"
	}
	d := time.Since(time.Unix(int64(ts), 0)).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String()
}

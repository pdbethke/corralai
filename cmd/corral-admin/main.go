// SPDX-License-Identifier: Elastic-2.0

// Command corral-admin is the operator client for a corralai brain: the
// privileged sibling of corral-observe. It both WATCHES the swarm (a live
// console, like the observer but with writes enabled) and ISSUES COMMANDS to it
// (instruct agents, mint observer tokens, manage principals, drive missions).
//
// The console is the same reverse proxy as the observer (internal/console) in
// read-write mode; the command verbs are a thin MCP client to the brain's
// operator tools. Both authenticate with an OPERATOR token (--token /
// CORRAL_TOKEN); principal-management verbs additionally require that token to
// be a superuser (the brain enforces this).
//
// Usage:
//
//	corral-admin ui                              # privileged live console (writes enabled)
//	corral-admin instruct <agent> <text...>      # tell an agent what to do
//	corral-admin status                          # active agents, claims, recent work
//	corral-admin whoami                          # who the brain sees you as
//	corral-admin mint-observer [--ttl 24h] [--principal x]
//	corral-admin member  list | add <email> | super <email> [--off] | create-super [email] | remove <email>
//	corral-admin mission list | status <id> | create <directive...>
//	corral-admin proposals list | show <id> | approve <id> | reject <id> --reason "..."
//
// Global flags (all verbs): --brain/CORRAL_BRAIN, --token/CORRAL_TOKEN, --json.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/console"
	"github.com/pdbethke/corralai/internal/pdftext"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log.SetFlags(0)
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "ui":
		cmdUI(rest)
	case "instruct":
		cmdInstruct(rest)
	case "status":
		cmdStatus(rest)
	case "whoami":
		cmdWhoami(rest)
	case "mint-observer":
		cmdMintObserver(rest)
	case "member":
		cmdMember(rest)
	case "mission":
		cmdMission(rest)
	case "findings":
		cmdFindings(rest)
	case "resolve-findings":
		cmdResolveFindings(rest)
	case "review":
		cmdReview(rest)
	case "reference":
		cmdReference(rest)
	case "analyze":
		cmdAnalyze(rest)
	case "proposals":
		cmdProposals(rest)
	case "version", "--version", "-v":
		fmt.Println("corral-admin", version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "corral-admin: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

// ---- shared connection config (every verb) ----

type conn struct {
	brain string
	token string
	json  bool
}

func bind(fs *flag.FlagSet) *conn {
	c := &conn{}
	fs.StringVar(&c.brain, "brain", "", "brain base URL, e.g. https://brain.example (or CORRAL_BRAIN)")
	fs.StringVar(&c.token, "token", "", "operator token (or CORRAL_TOKEN)")
	fs.BoolVar(&c.json, "json", false, "print the raw JSON result")
	return c
}

func (c *conn) resolve() {
	c.brain = pick(c.brain, os.Getenv("CORRAL_BRAIN"))
	c.token = pick(c.token, os.Getenv("CORRAL_TOKEN"))
	if c.brain == "" || c.token == "" {
		fatal("--brain and --token are required (or set CORRAL_BRAIN / CORRAL_TOKEN)")
	}
}

// do resolves the connection, calls one tool, and prints either the raw JSON
// (--json) or the human-formatted summary.
func (c *conn) do(name string, args map[string]any, human func(json.RawMessage)) {
	c.resolve()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := dial(ctx, c.brain, c.token)
	if err != nil {
		fatal(err.Error())
	}
	defer cl.close()
	out, err := cl.call(ctx, name, args)
	if err != nil {
		fatal(err.Error())
	}
	if c.json {
		printJSON(out)
		return
	}
	human(out)
}

// ---- ui: the privileged live console ----

func cmdUI(args []string) {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	c := bind(fs)
	addr := fs.String("addr", "", "local listen address (default 127.0.0.1:8081, or CORRAL_OBSERVE_ADDR)")
	open := fs.Bool("open", false, "open the console in your browser")
	ping := fs.Bool("ping", false, "health self-check: probe the health endpoint and exit 0/1")
	parseFlags(fs, args)

	listen := pick(*addr, os.Getenv("CORRAL_OBSERVE_ADDR"), "127.0.0.1:8081")
	if *ping {
		os.Exit(console.Ping(listen))
	}
	c.resolve()
	h, err := console.New(c.brain, c.token, false) // read-write: action controls work
	if err != nil {
		fatal(err.Error())
	}
	srv := &http.Server{Addr: listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("corral-admin: PRIVILEGED console for %s — open http://%s", c.brain, listen) // #nosec G706 -- operator-controlled config, not user input
	if *open {
		go console.OpenBrowser("http://" + console.LocalDialHost(listen))
	}
	log.Fatal(srv.ListenAndServe())
}

// ---- instruct ----

func cmdInstruct(args []string) {
	fs := flag.NewFlagSet("instruct", flag.ExitOnError)
	c := bind(fs)
	parseFlags(fs, args)
	rest := fs.Args()
	if len(rest) < 2 {
		fatal("usage: corral-admin instruct <agent> <text...>")
	}
	target, text := rest[0], strings.Join(rest[1:], " ")
	c.do("send_instruction", map[string]any{"target": target, "text": text}, func(out json.RawMessage) {
		var r struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal(out, &r)
		fmt.Printf("✓ instructed %s (instruction #%d)\n", target, r.ID)
	})
}

// ---- status ----

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	c := bind(fs)
	parseFlags(fs, args)
	c.do("coordination_status", nil, func(out json.RawMessage) {
		var s struct {
			ActiveAgents []struct {
				Name, Role, Status string
			} `json:"active_agents"`
			LiveClaims []struct {
				Agent, Path string
			} `json:"live_claims"`
			RecentCompleted []struct {
				AgentName string `json:"agent_name"`
				Summary   string `json:"summary"`
			} `json:"recent_completed"`
		}
		_ = json.Unmarshal(out, &s)
		fmt.Printf("active agents (%d):\n", len(s.ActiveAgents))
		for _, a := range s.ActiveAgents {
			fmt.Printf("  %-18s %-10s %s\n", a.Name, dash(a.Role), dash(a.Status))
		}
		fmt.Printf("live claims (%d):\n", len(s.LiveClaims))
		for _, cl := range s.LiveClaims {
			fmt.Printf("  %-18s %s\n", cl.Agent, cl.Path)
		}
		if len(s.RecentCompleted) > 0 {
			fmt.Printf("recent completed (%d):\n", len(s.RecentCompleted))
			for _, rc := range s.RecentCompleted {
				fmt.Printf("  %-18s %s\n", rc.AgentName, rc.Summary)
			}
		}
	})
}

// ---- whoami ----

func cmdWhoami(args []string) {
	fs := flag.NewFlagSet("whoami", flag.ExitOnError)
	c := bind(fs)
	parseFlags(fs, args)
	c.do("whoami", nil, func(out json.RawMessage) {
		var w struct {
			Principal   string `json:"principal"`
			IsSuperuser bool   `json:"is_superuser"`
			Allowed     bool   `json:"allowed"`
		}
		_ = json.Unmarshal(out, &w)
		p := w.Principal
		if p == "" {
			p = "(unauthenticated — dev brain)"
		}
		fmt.Printf("principal: %s\nsuperuser: %v\nallowed:   %v\n", p, w.IsSuperuser, w.Allowed)
	})
}

// ---- mint-observer ----

func cmdMintObserver(args []string) {
	fs := flag.NewFlagSet("mint-observer", flag.ExitOnError)
	c := bind(fs)
	ttl := fs.Duration("ttl", 24*time.Hour, "token lifetime (e.g. 24h, 7d as 168h)")
	principal := fs.String("principal", "", "principal to scope the token to (default: you)")
	parseFlags(fs, args)
	a := map[string]any{"ttl_seconds": int(ttl.Seconds())}
	if *principal != "" {
		a["principal"] = *principal
	}
	c.do("mint_observer", a, func(out json.RawMessage) {
		var r struct {
			Token string `json:"token"`
			Usage string `json:"usage"`
		}
		_ = json.Unmarshal(out, &r)
		fmt.Println(r.Token) // token to stdout, so it pipes cleanly
		if r.Usage != "" {
			fmt.Fprintln(os.Stderr, r.Usage)
		}
	})
}

// ---- member (principals) ----

func cmdMember(args []string) {
	if len(args) == 0 {
		fatal("usage: corral-admin member <list|add|super|create-super|remove> ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		fs := flag.NewFlagSet("member list", flag.ExitOnError)
		c := bind(fs)
		parseFlags(fs, rest)
		c.do("list_principals", nil, func(out json.RawMessage) {
			var r struct {
				Principals []struct {
					Email       string `json:"email"`
					IsSuperuser bool   `json:"is_superuser"`
				} `json:"principals"`
			}
			_ = json.Unmarshal(out, &r)
			fmt.Printf("principals (%d):\n", len(r.Principals))
			for _, p := range r.Principals {
				role := "member"
				if p.IsSuperuser {
					role = "superuser"
				}
				fmt.Printf("  %-32s %s\n", p.Email, role)
			}
		})
	case "add":
		c, email := memberArg(rest, "add")
		c.do("add_member", map[string]any{"email": email}, okMsg("added "+email))
	case "remove":
		c, email := memberArg(rest, "remove")
		c.do("remove_principal", map[string]any{"email": email}, okMsg("removed "+email))
	case "super":
		fs := flag.NewFlagSet("member super", flag.ExitOnError)
		c := bind(fs)
		off := fs.Bool("off", false, "demote instead of promote")
		parseFlags(fs, rest)
		if fs.NArg() < 1 {
			fatal("usage: corral-admin member super <email> [--off]")
		}
		email := fs.Arg(0)
		verb := "promoted"
		if *off {
			verb = "demoted"
		}
		c.do("set_superuser", map[string]any{"email": email, "is_superuser": !*off}, okMsg(verb+" "+email))
	case "create-super":
		fs := flag.NewFlagSet("member create-super", flag.ExitOnError)
		c := bind(fs)
		parseFlags(fs, rest)
		a := map[string]any{}
		if fs.NArg() > 0 {
			a["email"] = fs.Arg(0)
		}
		c.do("create_superuser", a, func(out json.RawMessage) {
			var r struct {
				Email   string `json:"email"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(out, &r)
			fmt.Printf("✓ %s: %s\n", r.Email, r.Message)
		})
	default:
		fatal("unknown member subcommand %q (list|add|super|create-super|remove)", sub)
	}
}

// memberArg binds flags and pulls the single required <email> positional.
func memberArg(args []string, name string) (*conn, string) {
	fs := flag.NewFlagSet("member "+name, flag.ExitOnError)
	c := bind(fs)
	parseFlags(fs, args)
	if fs.NArg() < 1 {
		fatal("usage: corral-admin member %s <email>", name)
	}
	return c, fs.Arg(0)
}

// ---- mission ----

func cmdMission(args []string) {
	if len(args) == 0 {
		fatal("usage: corral-admin mission <list|status|create> ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		fs := flag.NewFlagSet("mission list", flag.ExitOnError)
		c := bind(fs)
		parseFlags(fs, rest)
		c.do("list_missions", nil, func(out json.RawMessage) {
			var r struct {
				Missions []struct {
					ID        int64  `json:"id"`
					Directive string `json:"directive"`
					Status    string `json:"status"`
				} `json:"missions"`
			}
			_ = json.Unmarshal(out, &r)
			fmt.Printf("missions (%d):\n", len(r.Missions))
			for _, m := range r.Missions {
				fmt.Printf("  #%-4d %-10s %s\n", m.ID, m.Status, m.Directive)
			}
		})
	case "status":
		fs := flag.NewFlagSet("mission status", flag.ExitOnError)
		c := bind(fs)
		parseFlags(fs, rest)
		if fs.NArg() < 1 {
			fatal("usage: corral-admin mission status <id>")
		}
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil {
			fatal("mission id must be a number: %v", err)
		}
		c.do("mission_status", map[string]any{"id": id}, func(out json.RawMessage) {
			printJSON(out) // phases/instructions are nested; show the structured detail
		})
	case "create":
		fs := flag.NewFlagSet("mission create", flag.ExitOnError)
		c := bind(fs)
		review := fs.Bool("review", false, "require a client review (accept/feedback) instead of auto-completing — enables sprints")
		parseFlags(fs, rest)
		if fs.NArg() < 1 {
			fatal("usage: corral-admin mission create [--review] <directive...>")
		}
		directive := strings.Join(fs.Args(), " ")
		c.do("create_mission", map[string]any{"directive": directive, "requires_review": *review}, func(out json.RawMessage) {
			var r struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal(out, &r)
			fmt.Printf("✓ created mission #%d: %s\n", r.ID, directive)
		})
	default:
		fatal("unknown mission subcommand %q (list|status|create)", sub)
	}
}

// ---- findings ----

func cmdFindings(args []string) {
	fs := flag.NewFlagSet("findings", flag.ExitOnError)
	c := bind(fs)
	mission := fs.Int64("mission", 0, "limit to one mission")
	status := fs.String("status", "", "filter by status: open|addressed|dismissed")
	parseFlags(fs, args)
	a := map[string]any{}
	if *mission != 0 {
		a["mission_id"] = *mission
	}
	if *status != "" {
		a["status"] = *status
	}
	c.do("list_findings", a, func(out json.RawMessage) {
		var r struct {
			Findings []struct {
				ID       int64  `json:"id"`
				Reporter string `json:"reporter"`
				Type     string `json:"type"`
				Severity string `json:"severity"`
				Target   string `json:"target"`
				Status   string `json:"status"`
			} `json:"findings"`
		}
		_ = json.Unmarshal(out, &r)
		rank := map[string]int{"critical": 3, "high": 2, "medium": 1, "low": 0}
		sort.SliceStable(r.Findings, func(i, j int) bool { return rank[r.Findings[i].Severity] > rank[r.Findings[j].Severity] })
		fmt.Printf("findings (%d):\n", len(r.Findings))
		for _, f := range r.Findings {
			fmt.Printf("  #%-4d %-9s %-12s %-18s %-10s %s\n", f.ID, f.Severity, f.Type, dash(f.Target), f.Status, f.Reporter)
		}
	})
}

// ---- resolve-findings (demo helper: auto-resolve open findings after a delay) ----

// cmdResolveFindings lists all open findings (optionally for one mission), sleeps an
// optional delay, then marks each one addressed (or dismissed). This exists so the
// demo-models scenario can drive the finding_resolved events that populate the
// model_comparison confirmation-rate column without requiring a human operator to
// manually call resolve_finding for each finding.
func cmdResolveFindings(args []string) {
	fs := flag.NewFlagSet("resolve-findings", flag.ExitOnError)
	c := bind(fs)
	mission := fs.Int64("mission", 0, "limit to one mission (0 = all)")
	outcome := fs.String("outcome", "addressed", "resolution outcome: addressed|dismissed")
	delay := fs.Duration("delay", 0, "sleep this long before resolving (e.g. 2m30s)")
	limit := fs.Int("limit", 50, "maximum number of findings to resolve")
	parseFlags(fs, args)

	if *outcome != "addressed" && *outcome != "dismissed" {
		fatal("--outcome must be addressed or dismissed")
	}
	if *delay > 0 {
		log.Printf("resolve-findings: waiting %s before resolving…", *delay)
		time.Sleep(*delay)
	}

	c.resolve()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl, err := dial(ctx, c.brain, c.token)
	if err != nil {
		fatal(err.Error())
	}
	defer cl.close()

	// list open findings
	listArgs := map[string]any{"status": "open"}
	if *mission != 0 {
		listArgs["mission_id"] = *mission
	}
	raw, err := cl.call(ctx, "list_findings", listArgs)
	if err != nil {
		fatal("list_findings: %v", err)
	}
	var listed struct {
		Findings []struct {
			ID int64 `json:"id"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		fatal("decode findings: %v", err)
	}
	n := len(listed.Findings)
	if n > *limit {
		n = *limit
	}
	if n == 0 {
		fmt.Println("resolve-findings: no open findings found — nothing to do")
		return
	}
	fmt.Printf("resolve-findings: resolving %d finding(s) as %q…\n", n, *outcome)
	resolved := 0
	for _, f := range listed.Findings[:n] {
		resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, rerr := cl.call(resolveCtx, "resolve_finding", map[string]any{"id": f.ID, "status": *outcome})
		resolveCancel()
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not resolve finding #%d: %v\n", f.ID, rerr)
			continue
		}
		fmt.Printf("  ✓ finding #%d → %s\n", f.ID, *outcome)
		resolved++
	}
	fmt.Printf("resolve-findings: done (%d/%d resolved)\n", resolved, n)
}

// ---- review (the human client's verdict) ----

func cmdReview(args []string) {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	c := bind(fs)
	accept := fs.Bool("accept", false, "accept the deliverable (mission done)")
	changes := fs.String("changes", "", "request changes with this feedback (opens the next sprint)")
	parseFlags(fs, args)
	if fs.NArg() < 1 {
		fatal(`usage: corral-admin review <mission-id> --accept | --changes "..."`)
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		fatal("mission id must be a number: %v", err)
	}
	if !*accept && *changes == "" {
		fatal(`specify --accept or --changes "..."`)
	}
	a := map[string]any{"id": id, "accept": *accept}
	if !*accept {
		a["feedback"] = *changes
	}
	c.do("review_mission", a, func(out json.RawMessage) {
		var r struct {
			Status string `json:"status"`
			Sprint int64  `json:"sprint"`
		}
		_ = json.Unmarshal(out, &r)
		fmt.Printf("✓ mission #%d is now %s (sprint %d)\n", id, r.Status, r.Sprint)
	})
}

// ---- reference (the RAG corpus) ----

func cmdReference(args []string) {
	if len(args) == 0 {
		fatal(`usage: corral-admin reference <add|look|list|search> ...`)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		fs := flag.NewFlagSet("reference add", flag.ExitOnError)
		c := bind(fs)
		text := fs.String("text", "", "raw text to ingest")
		file := fs.String("file", "", "local file to ingest as text")
		source := fs.String("source", "", "name for the source (required for text/file)")
		parseFlags(fs, rest)
		m := map[string]any{}
		switch {
		case *file != "":
			b, err := os.ReadFile(*file)
			if err != nil {
				fatal("read %s: %v", *file, err)
			}
			content, kind := string(b), "text"
			if pdftext.IsPDF(b) { // extract locally so we send text, not binary
				content, err = pdftext.Extract(b)
				if err != nil {
					fatal("extract pdf %s: %v", *file, err)
				}
				kind = "pdf"
			}
			m["text"] = content
			m["kind"] = kind
			m["source"] = pick(*source, *file)
		case *text != "":
			if *source == "" {
				fatal("--source required with --text")
			}
			m["text"], m["source"] = *text, *source
		case fs.NArg() > 0:
			m["url"] = fs.Arg(0)
			if *source != "" {
				m["source"] = *source
			}
		default:
			fatal(`usage: corral-admin reference add <url> | --file <path> | --text "..." --source <name>`)
		}
		c.do("add_reference", m, func(out json.RawMessage) {
			var r struct {
				Source string `json:"source"`
				Chunks int    `json:"chunks"`
			}
			_ = json.Unmarshal(out, &r)
			fmt.Printf("✓ added %q (%d chunks)\n", r.Source, r.Chunks)
		})
	case "list":
		fs := flag.NewFlagSet("reference list", flag.ExitOnError)
		c := bind(fs)
		parseFlags(fs, rest)
		c.do("list_references", nil, func(out json.RawMessage) {
			var r struct {
				Sources []struct {
					Source string `json:"source"`
					Kind   string `json:"kind"`
					Chunks int    `json:"chunks"`
				} `json:"sources"`
			}
			_ = json.Unmarshal(out, &r)
			fmt.Printf("reference sources (%d):\n", len(r.Sources))
			for _, s := range r.Sources {
				fmt.Printf("  %-10s %-4d %s\n", s.Kind, s.Chunks, s.Source)
			}
		})
	case "look":
		fs := flag.NewFlagSet("reference look", flag.ExitOnError)
		c := bind(fs)
		note := fs.String("note", "", "what you like about this look — style, layout, vibe (required)")
		source := fs.String("source", "", "name for the look (defaults to the URL)")
		parseFlags(fs, rest)
		url := ""
		if fs.NArg() > 0 {
			url = fs.Arg(0)
		}
		if *note == "" {
			fatal(`--note required: describe the style, e.g. reference look https://x.com --note "clean, minimal, lots of whitespace"`)
		}
		src := pick(*source, url)
		if src == "" {
			fatal("provide a URL or --source name for the look")
		}
		text := "Design reference — a look we like"
		if url != "" {
			text += " at " + url
		}
		text += ". Style and qualities to emulate: " + *note
		c.do("add_reference", map[string]any{"source": src, "kind": "look", "text": text}, func(out json.RawMessage) {
			fmt.Printf("✓ saved look %q for the designer to consult\n", src)
		})
	case "search":
		fs := flag.NewFlagSet("reference search", flag.ExitOnError)
		c := bind(fs)
		k := fs.Int("k", 5, "results")
		parseFlags(fs, rest)
		if fs.NArg() < 1 {
			fatal(`usage: corral-admin reference search "<query>"`)
		}
		c.do("search_reference", map[string]any{"query": strings.Join(fs.Args(), " "), "k": *k}, func(out json.RawMessage) {
			var r struct {
				Hits []struct {
					Source string  `json:"source"`
					Score  float64 `json:"score"`
					Text   string  `json:"text"`
				} `json:"hits"`
			}
			_ = json.Unmarshal(out, &r)
			fmt.Printf("hits (%d):\n", len(r.Hits))
			for _, h := range r.Hits {
				snip := h.Text
				if len(snip) > 90 {
					snip = snip[:90] + "…"
				}
				fmt.Printf("  %.3f  %-20s %s\n", h.Score, h.Source, snip)
			}
		})
	default:
		fatal("unknown reference subcommand %q (add|look|list|search)", sub)
	}
}

// ---- analyze (mission telemetry / DuckDB) ----

func cmdAnalyze(args []string) {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	c := bind(fs)
	sql := fs.String("sql", "", "ad-hoc read-only SELECT/WITH query (superuser)")
	parseFlags(fs, args)
	a := map[string]any{}
	switch {
	case *sql != "":
		a["sql"] = *sql
	case fs.NArg() > 0:
		a["report"] = fs.Arg(0)
	default:
		a["report"] = "kinds"
	}
	c.do("mission_analytics", a, func(out json.RawMessage) {
		var r struct {
			Columns []string `json:"columns"`
			Rows    [][]any  `json:"rows"`
		}
		_ = json.Unmarshal(out, &r)
		printTable(r.Columns, r.Rows)
	})
}

// ---- proposals (the learning loop's human gate) ----

// proposal mirrors learn.Proposal's JSON shape (no json tags on the source
// struct, so field names are the Go names) — only the fields this verb renders.
type proposal struct {
	ID           int64  `json:"ID"`
	Signature    string `json:"Signature"`
	Kind         string `json:"Kind"`
	Roles        string `json:"Roles"`
	Guidance     string `json:"Guidance"`
	SkillName    string `json:"SkillName"`
	SkillBody    string `json:"SkillBody"`
	Status       string `json:"Status"`
	RejectReason string `json:"RejectReason"`
	Count        int    `json:"Count"`
	Supersedes   int64  `json:"Supersedes"`
}

func cmdProposals(args []string) {
	if len(args) == 0 {
		fatal(`usage: corral-admin proposals <list|show|approve|reject> ...`)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		fs := flag.NewFlagSet("proposals list", flag.ExitOnError)
		c := bind(fs)
		status := fs.String("status", "", "filter by status: pending|approved|rejected")
		parseFlags(fs, rest)
		a := map[string]any{}
		if *status != "" {
			a["status"] = *status
		}
		c.do("list_proposals", a, func(out json.RawMessage) {
			var r struct {
				Proposals []proposal `json:"proposals"`
			}
			_ = json.Unmarshal(out, &r)
			cols := []string{"id", "signature", "count", "status", "skill"}
			rows := make([][]any, len(r.Proposals))
			for i, p := range r.Proposals {
				rows[i] = []any{p.ID, p.Signature, p.Count, p.Status, dashEm(p.SkillName)}
			}
			printTable(cols, rows)
		})
	case "show":
		fs := flag.NewFlagSet("proposals show", flag.ExitOnError)
		c := bind(fs)
		parseFlags(fs, rest)
		if fs.NArg() < 1 {
			fatal("usage: corral-admin proposals show <id>")
		}
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil {
			fatal("proposal id must be a number: %v", err)
		}
		c.do("list_proposals", nil, func(out json.RawMessage) {
			var r struct {
				Proposals []proposal `json:"proposals"`
			}
			_ = json.Unmarshal(out, &r)
			for _, p := range r.Proposals {
				if p.ID != id {
					continue
				}
				fmt.Printf("#%d  %s  (%s)\n", p.ID, p.Signature, p.Kind)
				fmt.Printf("roles:    %s\n", dashEm(p.Roles))
				fmt.Printf("count:    %d\n", p.Count)
				fmt.Printf("status:   %s\n", dashEm(p.Status))
				if p.Status == "rejected" {
					fmt.Printf("reason:   %s\n", dashEm(p.RejectReason))
				}
				if p.Supersedes != 0 {
					fmt.Printf("supersedes: #%d\n", p.Supersedes)
				}
				fmt.Println("\nguidance:")
				fmt.Println(dashEm(p.Guidance))
				if p.SkillName != "" {
					fmt.Printf("\nskill: %s\n", p.SkillName)
					fmt.Println(p.SkillBody)
				}
				return
			}
			fatal("no proposal #%d", id)
		})
	case "approve":
		fs := flag.NewFlagSet("proposals approve", flag.ExitOnError)
		c := bind(fs)
		guidanceOnly := fs.Bool("guidance-only", false, "promote only the guidance, skip the skill")
		skillOnly := fs.Bool("skill-only", false, "promote only the skill, skip the guidance")
		parseFlags(fs, rest)
		if fs.NArg() < 1 {
			fatal("usage: corral-admin proposals approve <id> [--guidance-only|--skill-only]")
		}
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil {
			fatal("proposal id must be a number: %v", err)
		}
		if *guidanceOnly && *skillOnly {
			fatal("specify at most one of --guidance-only, --skill-only")
		}
		c.do("approve_proposal", map[string]any{"id": id, "guidance_only": *guidanceOnly, "skill_only": *skillOnly}, func(out json.RawMessage) {
			var r struct {
				PromotedGuidanceSlug string `json:"promoted_guidance_slug"`
				SkillPath            string `json:"skill_path"`
				SkillRev             int64  `json:"skill_rev"`
			}
			_ = json.Unmarshal(out, &r)
			fmt.Printf("✓ approved proposal #%d\n", id)
			if r.PromotedGuidanceSlug != "" {
				fmt.Printf("  guidance → %s\n", r.PromotedGuidanceSlug)
			}
			if r.SkillPath != "" {
				fmt.Printf("  skill    → %s (rev %d)\n", r.SkillPath, r.SkillRev)
			}
		})
	case "reject":
		fs := flag.NewFlagSet("proposals reject", flag.ExitOnError)
		c := bind(fs)
		reason := fs.String("reason", "", "why this proposal is being dismissed")
		parseFlags(fs, rest)
		if fs.NArg() < 1 {
			fatal(`usage: corral-admin proposals reject <id> --reason "..."`)
		}
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil {
			fatal("proposal id must be a number: %v", err)
		}
		c.do("reject_proposal", map[string]any{"id": id, "reason": *reason}, okMsg(fmt.Sprintf("rejected proposal #%d", id)))
	default:
		fatal("unknown proposals subcommand %q (list|show|approve|reject)", sub)
	}
}

// printTable renders columns + rows as an aligned text table.
func printTable(cols []string, rows [][]any) {
	if len(cols) == 0 {
		fmt.Println("(no columns)")
		return
	}
	w := make([]int, len(cols))
	for i, c := range cols {
		w[i] = len(c)
	}
	cells := make([][]string, len(rows))
	for r, row := range rows {
		cells[r] = make([]string, len(cols))
		for i := range cols {
			v := ""
			if i < len(row) && row[i] != nil {
				v = fmt.Sprintf("%v", row[i])
			} else if i < len(row) {
				v = "-" // SQL NULL (e.g. confirm_pct with no resolutions) — not Go's "<nil>"
			}
			cells[r][i] = v
			if len(v) > w[i] {
				w[i] = len(v)
			}
		}
	}
	line := func(vals []string) {
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = fmt.Sprintf("%-*s", w[i], v)
		}
		fmt.Println("  " + strings.Join(parts, "  "))
	}
	line(cols)
	for _, rc := range cells {
		line(rc)
	}
	fmt.Printf("(%d rows)\n", len(rows))
}

// ---- helpers ----

func okMsg(msg string) func(json.RawMessage) {
	return func(_ json.RawMessage) { fmt.Println("✓ " + msg) }
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// dashEm renders an empty string as "-" (SQL-NULL-style dash for the
// proposals verbs — never Go's "<nil>").
func dashEm(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func pick(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseFlags parses flags that may appear before OR after positional args — Go's
// flag.Parse otherwise stops at the first positional, so `verb <id> --flag` would
// silently drop --flag. Flags (and the value a non-bool flag consumes) are
// reordered ahead of positionals; fs.Args() then holds the positionals.
func parseFlags(fs *flag.FlagSet, args []string) {
	isBool := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			isBool[f.Name] = true
		}
	})
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' && a != "--" {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if strings.ContainsRune(name, '=') { // value attached (--flag=v)
				continue
			}
			if !isBool[name] && i+1 < len(args) { // consume the value token
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	_ = fs.Parse(append(flags, pos...))
}

func printJSON(raw json.RawMessage) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		fmt.Println(string(raw))
		return
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "corral-admin: "+format+"\n", a...)
	os.Exit(1)
}

func usage() {
	fmt.Fprint(os.Stderr, `corral-admin — operator client for a corralai brain

  corral-admin ui [--addr a] [--open]          privileged live console (writes enabled)
  corral-admin instruct <agent> <text...>      tell an agent what to do
  corral-admin status                          active agents, claims, recent work
  corral-admin whoami                          who the brain sees you as
  corral-admin mint-observer [--ttl 24h] [--principal x]
  corral-admin member  list | add <email> | super <email> [--off] | create-super [email] | remove <email>
  corral-admin mission list | status <id> | create <directive...>
  corral-admin review <id> --accept | --changes "..."
  corral-admin findings [--mission N] [--status open]
  corral-admin resolve-findings [--delay 2m] [--mission N] [--outcome addressed|dismissed]
  corral-admin reference add <url> | --file <path> | --text "..." --source <n> | list | search "<q>"
  corral-admin reference look <url> --note "..."   (a design "look" for the designer)
  corral-admin analyze [missions|agents|kinds|findings|replans|sprints] | --sql "SELECT ..."
  corral-admin proposals list [--status pending|approved|rejected]
  corral-admin proposals show <id>
  corral-admin proposals approve <id> [--guidance-only|--skill-only]
  corral-admin proposals reject <id> --reason "..."

Global flags: --brain/CORRAL_BRAIN  --token/CORRAL_TOKEN  --json
`)
}

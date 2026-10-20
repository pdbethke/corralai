// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/recordview"
)

// runUI serves a local view of the record: the record pages, rendered by
// internal/recordview (the same pages as the public record), plus the seal.
// Read-only unless --write, which adds the guarded write routes.
//
// WHAT IT IS NOT. internal/ui is the BRAIN's cockpit — a headless daemon
// serving a signed /console bundle to thin clients, with ~30 collaborators
// (coordination, bus, missions, queue, gateway). Wiring that into the audit CLI
// would drag the entire platform surface in behind it, which is the opposite of
// what corral needs: the certify path and the coordination platform are already
// near-equal halves of this repository sharing one binary.
//
// So this reads exactly what `corral seal` reads and nothing else — the same
// sealReader, the same DSN resolution, the same rows. No brain, no queue, no
// credentials, no writes. If `corral seal` can answer it, this can show it; if
// it cannot, neither can this.
//
// LOOPBACK BY DEFAULT, and it says so when asked to bind wider: the ledger
// names repositories, file paths and their weakest spots, which is a map of
// where someone's tests are thinnest. That is not a thing to serve to a network
// by accident.
func runUI(args []string, open func(dsn string) (sealReader, error), stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dsn := fs.String("db", "", "what to read: a ledger directory (the seal, the chain and the reviews) or a warehouse file / md:<db> (the seal only) — default $CORRAL_LEDGER, else ./.corral/ledger, the same resolution corral seal and corral scans use")
	addr := fs.String("addr", "127.0.0.1:8787", "local listen address. Loopback by default ON PURPOSE: the ledger is a map of where a codebase's tests are thinnest")
	once := fs.Bool("print-url", false, "print the URL and exit without serving (for scripts and smoke tests)")
	write := fs.Bool("write", false, "let this page WRITE: adjudicate findings and recheck them, by running corral's own subcommands. Loopback only; prints a URL carrying a launch token valid until the server exits — anyone with that URL can write verdicts in your name. The URL is printed, never opened, so the token stays off every process's command line; over SSH, tunnel the same port on both ends (ssh -L 8787:127.0.0.1:8787). A click on Confirm or Refute is a verdict recorded as you")
	openBrowser := fs.Bool("open", false, "with --write, also open the URL in a browser. A convenience for a desktop: it hands the URL, token included, to the opener as a command-line argument, which other local processes can read while it runs")
	repoDir := fs.String("repo", ".", "with --write, the checkout a finding's reproduction is rechecked against (its HEAD, in the sandbox — see corral review recheck)")
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}

	target := strings.TrimSpace(*dsn)
	if target == "" {
		target = defaultLedgerDir(".")
	}
	st, err := open(target)
	if err != nil {
		fmt.Fprintln(stderr, "corral ui:", err)
		return 1
	}
	defer st.Close() //nolint:errcheck

	var writer *uiWriter
	url := "http://" + *addr
	if *write {
		if !isLoopback(*addr) {
			fmt.Fprintf(stderr, "corral ui: --write refused on %s: write mode is loopback only (127.0.0.1, [::1] or localhost)\n", *addr)
			return 2
		}
		// Write mode is a verdict queue read from a ledger directory. With
		// none (a warehouse, md:, or an absent default) the page would read
		// "0 findings … Nothing is waiting for a verdict" — could-not-read
		// shown as a measured zero. Refuse instead.
		if uiLedgerDir(target) == "" {
			fmt.Fprintf(stderr, "corral ui: --write needs a ledger directory; %s is not one\n", target)
			return 2
		}
		hosts, herr := uiAllowedHosts(*addr)
		if herr != nil {
			fmt.Fprintln(stderr, "corral ui:", herr)
			return 2
		}
		tok, terr := newUIToken()
		if terr != nil {
			fmt.Fprintln(stderr, "corral ui:", terr)
			return 1
		}
		absRepo, aerr := filepath.Abs(*repoDir)
		if aerr != nil {
			fmt.Fprintln(stderr, "corral ui:", aerr)
			return 2
		}
		writer = &uiWriter{token: tok, hosts: hosts, repo: absRepo, ledgerDir: uiLedgerDir(target), cli: corralSubprocess}
		url += "/#t=" + tok
		fmt.Fprintf(stdout, "corral ui: WRITE mode, reading %s — open (this URL carries the launch token; anyone with it can write verdicts in your name):\n  %s\n  rechecks run against %s\n", target, url, absRepo)
	} else {
		if !isLoopback(*addr) {
			fmt.Fprintf(stderr, "corral ui: WARNING serving on %s, which is not loopback — the ledger names repositories, file paths and their weakest files\n", *addr)
		}
		fmt.Fprintf(stdout, "corral ui: reading %s — open %s\n", target, url)
	}
	if *once {
		return 0
	}
	if writer != nil && *openBrowser {
		if oerr := uiOpenBrowser(url); oerr != nil {
			fmt.Fprintf(stderr, "corral ui: could not open a browser (%v) — open the URL above yourself\n", oerr)
		}
	}

	srv := &http.Server{Addr: *addr, Handler: uiHandlerWith(st, uiLedgerDir(target), writer), ReadHeaderTimeout: 10 * time.Second}
	if err := uiServe(srv); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, "corral ui:", err)
		return 1
	}
	return 0
}

// uiHandler is the read-only page: exactly what `corral ui` served before
// --write existed.
func uiHandler(st sealReader, ledgerDir string) http.Handler {
	return uiHandlerWith(st, ledgerDir, nil)
}

// uiHandlerWith serves the page; w != nil adds the write routes and, in
// that mode only, refuses any request whose Host this server does not
// answer to (DNS rebinding).
func uiHandlerWith(st sealReader, ledgerDir string, wr *uiWriter) http.Handler {
	mux := http.NewServeMux()
	if wr != nil {
		mux.HandleFunc("/api/whoami", wr.guard(func(w http.ResponseWriter, r *http.Request) {
			by := ""
			if u, err := user.Current(); err == nil {
				by = u.Username
			}
			writeJSON(w, http.StatusOK, map[string]string{"by": by, "repo": wr.repo})
		}))
		mux.HandleFunc("/api/recheck", wr.guard(wr.recheck))
		mux.HandleFunc("/api/adjudicate", wr.guard(wr.adjudicate))
		mux.HandleFunc("/api/cited", wr.cited) // a read: Host-gated by hostGate, no token
	}
	// Everything else is the record: recordview's pages, the same ones the
	// public record is built from, read fresh on every request so a
	// verdict written from another terminal shows on reload. GET and HEAD
	// only, and only the paths recordview rendered; anything else is 404.
	mux.Handle("/", recordview.Handler(uiSite(st, ledgerDir, wr != nil)))
	return hostGate(wr, mux)
}

// hostGate refuses, in write mode, every request whose Host the server does
// not answer to. Read-only mode is returned unchanged.
func hostGate(wr *uiWriter, next http.Handler) http.Handler {
	if wr == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !wr.hostOK(r) {
			writeJSON(w, http.StatusMisdirectedRequest, map[string]string{"error": "this server does not answer to that Host"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopback reports whether addr binds only to the local machine. A hostname
// that does not resolve is treated as NOT loopback: the warning is cheap and a
// missed warning is not.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false // ":8787" binds every interface
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

// uiServe runs the server. A variable so tests can drive runUI past
// --print-url without binding a port.
var uiServe = func(srv *http.Server) error { return srv.ListenAndServe() }

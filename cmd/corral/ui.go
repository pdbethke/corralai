// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

//go:embed uiweb/*
var uiWeb embed.FS

// runUI serves a local, read-only view of the scan ledger.
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
	dsn := fs.String("db", "", "warehouse to read (default: $CORRALAI_SCANS_DB, else ~/.claude/corralai_scans.duckdb — the same resolution `corral seal` and `corral scans` use)")
	addr := fs.String("addr", "127.0.0.1:8787", "local listen address. Loopback by default ON PURPOSE: the ledger is a map of where a codebase's tests are thinnest")
	once := fs.Bool("print-url", false, "print the URL and exit without serving (for scripts and smoke tests)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	target := strings.TrimSpace(*dsn)
	if target == "" {
		target = defaultScanDSN()
	}
	st, err := open(target)
	if err != nil {
		fmt.Fprintln(stderr, "corral ui:", err)
		return 1
	}
	defer st.Close() //nolint:errcheck

	if !isLoopback(*addr) {
		fmt.Fprintf(stderr, "corral ui: WARNING serving on %s, which is not loopback — the ledger names repositories, file paths and their weakest files\n", *addr)
	}
	url := "http://" + *addr
	fmt.Fprintf(stdout, "corral ui: reading %s — open %s\n", target, url)
	if *once {
		return 0
	}

	srv := &http.Server{Addr: *addr, Handler: uiHandler(st), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, "corral ui:", err)
		return 1
	}
	return 0
}

// uiHandler serves the page and the one endpoint behind it.
//
// Two routes, deliberately: a UI that grows endpoints grows a schema, and this
// one's contract is "whatever the seal view already means".
func uiHandler(st sealReader) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/seal", func(w http.ResponseWriter, r *http.Request) {
		rows, err := st.SealRows(r.Context(), strings.TrimSpace(r.URL.Query().Get("repo")))
		if err != nil {
			// A read failure is reported as one. An empty list would render as
			// "this codebase has no audited files", which is a different and
			// much more comforting claim than "the ledger could not be read".
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].ProvenMissed != rows[j].ProvenMissed {
				return rows[i].ProvenMissed > rows[j].ProvenMissed // proven gaps first: the earned findings
			}
			return rows[i].KillRate < rows[j].KillRate
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rows)
	})
	sub, err := fs.Sub(uiWeb, "uiweb")
	if err != nil {
		// The page is embedded at build time, so this cannot fail in a shipped
		// binary — but serving the API with no page at all is a worse answer
		// than saying so.
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "corral ui: embedded page unavailable: "+err.Error(), http.StatusInternalServerError)
		})
		return mux
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return mux
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

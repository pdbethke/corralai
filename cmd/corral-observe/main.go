// SPDX-License-Identifier: Elastic-2.0

// Command corral-observe is the read-only observer for a corralai brain.
//
// It is a thin, credentialed reverse proxy (internal/console in read-only mode).
// It holds a READ-ONLY observer token (minted by the brain's mint_observer tool,
// or by `corral-admin mint-observer`), injects it as an Authorization: Bearer
// header on every request, and forwards to a remote brain's live swarm UI. The
// browser talks only to the observer, so the token never reaches it; and because
// the token is read-only, the brain 403s any action. As defense in depth the
// observer also refuses non-GET methods locally, so its read-only guarantee
// holds even if it is handed a non-read-only token by mistake.
//
// Usage:
//
//	corral-observe --brain https://brain.example --token cdt_... [--open]
//
// Configuration (a flag wins over its env var; the env vars exist for
// containers, where flags are awkward):
//
//	--brain / CORRAL_BRAIN          brain base URL (required)
//	--token / CORRAL_TOKEN          read-only observer token (required)
//	--addr  / CORRAL_OBSERVE_ADDR   local listen address (default 127.0.0.1:8080)
//	--open                          open the console in your browser (local use)
//	--ping                          health self-check: probe the health endpoint, exit 0/1
//
// The observer forwards the brain's hostname as the upstream Host, so when the
// brain is behind a domain that domain must be in the brain's
// CORRALAI_ALLOWED_HOSTS (the brain's Host allowlist).
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pdbethke/corralai/internal/console"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var brainFlag, tokenFlag, addrFlag string
	var open, ping, ver bool
	flag.StringVar(&brainFlag, "brain", "", "brain base URL, e.g. https://brain.example (or CORRAL_BRAIN)")
	flag.StringVar(&tokenFlag, "token", "", "read-only observer token (or CORRAL_TOKEN)")
	flag.StringVar(&addrFlag, "addr", "", "local listen address (default 127.0.0.1:8080, or CORRAL_OBSERVE_ADDR)")
	flag.BoolVar(&open, "open", false, "open the console in your browser (local use)")
	flag.BoolVar(&ping, "ping", false, "health self-check: probe the health endpoint and exit 0 (healthy) or 1")
	flag.BoolVar(&ver, "version", false, "print version and exit")
	flag.Parse()
	if ver {
		fmt.Println("corral-observe", version)
		return
	}

	addr := pick(addrFlag, os.Getenv("CORRAL_OBSERVE_ADDR"), "127.0.0.1:8080")
	if ping {
		os.Exit(console.Ping(addr))
	}

	brainURL := pick(brainFlag, os.Getenv("CORRAL_BRAIN"))
	token := pick(tokenFlag, os.Getenv("CORRAL_TOKEN"))
	if brainURL == "" || token == "" {
		log.Fatal("corral-observe: --brain and --token are required (or set CORRAL_BRAIN / CORRAL_TOKEN)")
	}

	h, err := console.New(brainURL, token, true) // read-only
	if err != nil {
		log.Fatalf("corral-observe: %v", err)
	}

	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("corral-observe: read-only console for %s — open http://%s", brainURL, addr) // #nosec G706 -- operator-controlled config, not user input
	if open {
		go console.OpenBrowser("http://" + console.LocalDialHost(addr))
	}
	log.Fatal(srv.ListenAndServe())
}

func pick(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

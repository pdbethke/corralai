// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pdbethke/corralai/internal/attest"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/statedir"
)

// keyPub derives the public key a key row's file holds, without any path
// that could create one — the check a person compares with LEDGER_PUBKEY.
var keyPub = map[statedir.File]func([]byte) (ed25519.PublicKey, error){
	statedir.CertifyKey: func(b []byte) (ed25519.PublicKey, error) {
		k, err := buildstore.ParseSigningKeySeed(b)
		if err != nil {
			return nil, err
		}
		return k.Public().(ed25519.PublicKey), nil
	},
	statedir.BrainKey: func(b []byte) (ed25519.PublicKey, error) {
		k, err := attest.ParseKeySeed(b)
		return k.Pub, err
	},
}

func runState(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: corral state migrate [--from DIR] [--dry-run] | corral state paths"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return 0
	case "paths":
		if len(args) > 1 {
			if args[1] == "-h" || args[1] == "-help" || args[1] == "--help" {
				fmt.Fprintln(stdout, "usage: corral state paths  (takes no arguments; prints each file's root, name, override variable and resolved path)")
				return 0
			}
			fmt.Fprintln(stderr, "usage: corral state paths  (takes no arguments)")
			return 2
		}
		for _, f := range statedir.Files() {
			p, err := statedir.Path(f)
			if err != nil {
				p = "REFUSED: " + err.Error()
			}
			fmt.Fprintf(stdout, "%-6s %-32s %-30s %s\n", f.Root, f.Name, f.Override, p)
		}
		return 0
	case "migrate":
		return runStateMigrate(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "corral state: unknown subcommand %q\n", args[0])
	return 2
}

func runStateMigrate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("state migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "the directory corral's state is in now (default: the pre-statedir location)")
	dry := fs.Bool("dry-run", false, "print what would move and change nothing")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *from == "" {
		d, err := statedir.LegacyDir()
		if err != nil {
			fmt.Fprintln(stderr, "corral state migrate:", err)
			return 1
		}
		*from = d
	}
	moves, conflicts, err := statedir.PlanMigration(*from)
	if err != nil {
		fmt.Fprintln(stderr, "corral state migrate:", err)
		return 1
	}
	if len(conflicts) > 0 {
		for _, c := range conflicts {
			fmt.Fprintf(stderr, "conflict: %s (%d bytes, %s) and %s (%d bytes, %s) both exist\n",
				c.From, c.FromInfo.Size(), c.FromInfo.ModTime().Format("2006-01-02 15:04"),
				c.To, c.ToInfo.Size(), c.ToInfo.ModTime().Format("2006-01-02 15:04"))
		}
		fmt.Fprintln(stderr, "corral state migrate: nothing moved — resolve each conflict (keep one copy) and run again")
		return 1
	}
	if len(moves) == 0 {
		fmt.Fprintln(stdout, "nothing to move")
		return 0
	}
	before := printKeys(moves, true, stdout, stderr)
	for _, m := range moves {
		fmt.Fprintf(stdout, "%s → %s\n", m.From, m.To)
		for _, s := range m.Sidecars {
			fmt.Fprintf(stdout, "  + %s\n", s)
		}
	}
	if *dry {
		fmt.Fprintln(stdout, "dry run: nothing moved")
		return 0
	}
	if err := statedir.ApplyMigration(moves); err != nil {
		fmt.Fprintln(stderr, "corral state migrate:", err)
		return 1
	}
	after := printKeys(moves, false, stdout, stderr)
	for k, v := range before {
		if after[k] != v {
			fmt.Fprintf(stderr, "corral state migrate: %s's public key changed in the move (%s → %s)\n", k, v, after[k])
			return 1
		}
	}
	fmt.Fprintln(stdout, "done. The certify public key above must equal LEDGER_PUBKEY for the ledger you sign.")
	return 0
}

// printKeys prints each key row's public key, read from the source before
// the move and the destination after it.
func printKeys(moves []statedir.Move, before bool, stdout, stderr io.Writer) map[string]string {
	out := map[string]string{}
	for _, m := range moves {
		pubOf, ok := keyPub[m.File]
		if !ok {
			continue
		}
		p, when := m.To, "after "
		if before {
			p, when = m.From, "before"
		}
		raw, err := os.ReadFile(p) // #nosec G304 -- a statedir table row
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", p, err)
			continue
		}
		pub, err := pubOf(raw)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", p, err)
			continue
		}
		out[m.File.Name] = hex.EncodeToString(pub)
		fmt.Fprintf(stdout, "%s %s public key %s\n", when, m.File.Name, out[m.File.Name])
	}
	return out
}

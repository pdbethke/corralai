// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/pdbethke/corralai/internal/creds"
)

// runSecret implements `corral secret set|get|list|rm <NAME>`. Values are read
// from stdin (never args) to keep them out of ps/shell history; list prints
// names only, and set confirms with a redacted fingerprint — never the raw
// value.
func runSecret(args []string, stdin io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: corral secret set|get|list|rm <NAME>")
	}
	// HELP AND NAME CHECKS COME BEFORE THE KEYSTORE AND BEFORE STDIN. A help
	// token is honoured as the first argument (`secret -h`) and as the argument
	// right after a leaf (`secret set -h`, `get -h`, `rm -h`); before this,
	// `secret set -h` stored a secret named "-h" read from stdin, `get -h`
	// looked one up and `rm -h` removed one. The generator's leaf derivation
	// runs `secret set -h`, so that was also a way for doc generation to write
	// to a keystore.
	//
	// A secret NAME that begins with "-" is refused outright rather than made
	// storable through some escape: names are env-var names
	// (ANTHROPIC_API_KEY, CORRALAI_BRAIN_TOKEN — the env backend is first in the
	// chain), which can never begin with "-", so the only thing such a name can
	// be is a mistyped flag. Refusing it also keeps `secret set --force` from
	// silently storing a secret called "--force". creds itself does not validate
	// names; this is the CLI's door, and it is the only one.
	if wantsHelp(args[:1]) {
		fmt.Fprintln(out, "usage: corral secret set|get|list|rm <NAME>  (set reads the value from stdin — never a CLI arg)")
		return nil
	}
	switch args[0] {
	case "set", "get", "rm":
		if len(args) >= 2 && wantsHelp(args[1:2]) {
			if args[0] == "set" {
				fmt.Fprintln(out, "usage: corral secret set <NAME>  (value read from stdin — never a CLI arg)")
			} else {
				fmt.Fprintf(out, "usage: corral secret %s <NAME>\n", args[0])
			}
			return nil
		}
		if len(args) == 2 && strings.HasPrefix(args[1], "-") {
			return fmt.Errorf("%q is not a secret name (names are env-var style, e.g. ANTHROPIC_API_KEY, and never begin with \"-\") — usage: corral secret %s <NAME>", args[1], args[0])
		}
	}
	s, err := creds.Open()
	if err != nil {
		return err
	}
	switch args[0] {
	case "set":
		if len(args) != 2 {
			return fmt.Errorf("usage: corral secret set <NAME>  (value read from stdin — never a CLI arg)")
		}
		name := args[1]
		val, err := readSecretValue(stdin)
		if err != nil {
			return err
		}
		if val == "" {
			return fmt.Errorf("no value read from stdin")
		}
		if err := s.Set(name, val); err != nil {
			return err
		}
		fmt.Fprintf(out, "stored %s (%s)\n", name, creds.Redact(val))
		return nil
	case "get":
		if len(args) != 2 {
			return fmt.Errorf("usage: corral secret get <NAME>")
		}
		v, ok, err := s.Get(args[1])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no secret %q", args[1])
		}
		fmt.Fprintln(out, v)
		return nil
	case "list":
		names, err := s.List()
		if err != nil {
			return err
		}
		for _, n := range names {
			fmt.Fprintln(out, n)
		}
		return nil
	case "rm":
		if len(args) != 2 {
			return fmt.Errorf("usage: corral secret rm <NAME>")
		}
		return s.Remove(args[1])
	default:
		return fmt.Errorf("unknown secret subcommand %q (set|get|list|rm)", args[0])
	}
}

// readSecretValue reads one line (the secret) from stdin, trimming the trailing
// newline. Reading from stdin (not argv) keeps the value out of ps/shell history.
func readSecretValue(stdin io.Reader) (string, error) {
	r := bufio.NewReader(stdin)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

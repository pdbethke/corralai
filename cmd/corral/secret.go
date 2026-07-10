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

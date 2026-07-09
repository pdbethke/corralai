// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestSecretSetListNoValueLeak(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())

	var out bytes.Buffer
	// set reads the value from stdin, not args
	if err := runSecret([]string{"set", "OPENAI_API_KEY"}, strings.NewReader("sk-secret-value\n"), &out); err != nil {
		t.Fatalf("set: %v", err)
	}
	out.Reset()
	if err := runSecret([]string{"list"}, nil, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "OPENAI_API_KEY") {
		t.Fatalf("list should show the name, got %q", out.String())
	}
	if strings.Contains(out.String(), "sk-secret-value") {
		t.Fatal("list leaked the secret VALUE")
	}
}

// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeServer(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "srv")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o700); err != nil { // #nosec G306 -- test helper script must be executable
		t.Fatal(err)
	}
	return p
}

func TestCheckMCPSpeaksJSONRPC(t *testing.T) {
	good := fakeServer(t, `
read l; echo '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"x","version":"1"}}}'
read l
read l; echo '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"list_audit_findings"},{"name":"get_audit_finding"}]}}'
`)
	if err := CheckMCP(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	missing := fakeServer(t, `
read l; echo '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"x","version":"1"}}}'
read l
read l; echo '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"list_audit_findings"}]}}'
`)
	if err := CheckMCP(context.Background(), missing); err == nil || !strings.Contains(err.Error(), "get_audit_finding") {
		t.Fatalf("%v", err)
	}
	if err := CheckMCP(context.Background(), fakeServer(t, "exit 0\n")); err == nil {
		t.Fatal("a server that exits must fail")
	}
	if err := CheckMCP(context.Background(), filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing binary must fail")
	}
}

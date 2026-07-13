// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

// TestBrowserManagerSweepsIdlePages proves BrowserManager.pages can't grow
// unbounded: mirrors WorkerSessions' lazy-TTL-sweep-on-access pattern. A live
// *rod.Page needs Chromium, so this drives the bookkeeping directly via the
// clock seam and nil-page-safe sentinel entries — no real tab launched.
func TestBrowserManagerSweepsIdlePages(t *testing.T) {
	bm := NewBrowserManager("127.0.0.1:9019")
	base := time.Unix(1_000_000, 0)
	bm.now = func() time.Time { return base }

	bm.trackForTest("old", base.Add(-2*browserPageTTL))
	bm.trackForTest("fresh", base)

	bm.sweepIdle(base)

	if _, ok := bm.pages["old"]; ok {
		t.Fatal("stale agent 'old' not evicted")
	}
	if _, ok := bm.pages["fresh"]; !ok {
		t.Fatal("fresh agent wrongly evicted")
	}
}

// fakeResolver installs a deterministic lookupIP for the duration of the test —
// the suite does no real DNS. Unlisted hosts return an error (unresolvable).
func fakeResolver(t *testing.T, m map[string][]string) {
	t.Helper()
	orig := lookupIP
	t.Cleanup(func() { lookupIP = orig })
	lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		ips, ok := m[host]
		if !ok {
			return nil, fmt.Errorf("fakeResolver: no record for %q", host)
		}
		out := make([]net.IPAddr, 0, len(ips))
		for _, s := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(s)})
		}
		return out, nil
	}
}

// TestMetadataOrLinkLocal proves the browser predicate stays COMPLETE after the
// IP-literal set was moved to netguard (F2 DRY): the Alibaba/AWS-IPv6 IMDS
// literals come from netguard.IsIMDSLiteral, link-local (incl. 169.254.169.254)
// from the local predicate, and loopback/private/public stay allowed so a tester
// can drive the app on localhost.
func TestMetadataOrLinkLocal(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"100.100.100.100", true}, // Alibaba IMDS — via shared netguard set
		{"fd00:ec2::254", true},   // AWS IPv6 IMDS — via shared netguard set
		{"169.254.169.254", true}, // AWS/GCP IMDS — link-local predicate
		{"fe80::1", true},         // link-local unicast
		{"127.0.0.1", false},      // loopback stays allowed (localhost testing)
		{"10.0.0.5", false},       // private stays allowed
		{"8.8.8.8", false},        // public
	}
	for _, c := range cases {
		if got := metadataOrLinkLocal(net.ParseIP(c.ip)); got != c.blocked {
			t.Errorf("metadataOrLinkLocal(%s)=%v want %v", c.ip, got, c.blocked)
		}
	}
}

func TestGuardNavigateURL(t *testing.T) {
	const brain = "127.0.0.1:9019"
	// Deterministic DNS: the key bypass is a public-DNS alias whose A record is
	// the cloud metadata IP (e.g. 169-254-169-254.sslip.io).
	fakeResolver(t, map[string][]string{
		"localhost":       {"127.0.0.1"},
		"example.com":     {"93.184.216.34"},
		"public.test":     {"93.184.216.34"},
		"imds-alias.test": {"169.254.169.254"}, // DNS alias → AWS IMDS
		"ipv6-imds.test":  {"fd00:ec2::254"},   // DNS alias → AWS IPv6 IMDS
		"mixed.test":      {"93.184.216.34", "169.254.169.254"},
	})
	cases := []struct {
		name    string
		url     string
		blocked bool
	}{
		// allowed: the app under test on localhost (a DIFFERENT port than the brain)
		{"localhost app", "http://127.0.0.1:3000/dashboard", false},
		{"localhost name", "http://localhost:8080/", false},
		{"private lan", "http://192.168.1.50:5173/", false},
		{"public https", "https://example.com/", false},
		{"public dns host", "http://public.test/", false},
		// blocked: cloud metadata (IMDS) — IAM credential theft
		{"aws imds", "http://169.254.169.254/latest/meta-data/iam/", true},
		{"ecs imds", "http://169.254.170.2/v2/credentials", true},
		{"gcp metadata host", "http://metadata.google.internal/computeMetadata/v1/", true},
		{"alibaba imds", "http://100.100.100.100/latest/meta-data/", true},
		{"aws ipv6 imds literal", "http://[fd00:ec2::254]/latest/", true},
		// blocked: the DNS-alias bypass — a public hostname resolving to metadata/link-local
		{"dns alias to imds", "http://imds-alias.test/latest/meta-data/", true},
		{"dns alias to ipv6 imds", "http://ipv6-imds.test/latest/", true},
		{"dns alias one-of-many is imds", "http://mixed.test/", true},
		// blocked: the brain's own admin/MCP surface (loopback on the brain port)
		{"brain loopback ip", "http://127.0.0.1:9019/api/state", true},
		{"brain localhost", "http://localhost:9019/mcp/", true},
		// blocked: dangerous schemes
		{"file scheme", "file:///etc/passwd", true},
		{"chrome scheme", "chrome://settings", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := guardNavigateURL(c.url, brain)
			if c.blocked && err == nil {
				t.Errorf("expected %q to be BLOCKED, but it was allowed", c.url)
			}
			if !c.blocked && err != nil {
				t.Errorf("expected %q to be ALLOWED, but it was blocked: %v", c.url, err)
			}
		})
	}
}

// TestGuardNavigateURLSelfAddrNonLoopback proves the brain is blocked when it
// listens on a non-loopback address: the URL host equal to selfAddr's host (on
// the brain port) is refused, and the unspecified addresses 0.0.0.0/:: are
// refused too.
func TestGuardNavigateURLSelfAddrNonLoopback(t *testing.T) {
	const brain = "10.0.0.5:9019"
	fakeResolver(t, map[string][]string{
		"app.internal": {"93.184.216.34"},
	})
	cases := []struct {
		name    string
		url     string
		blocked bool
	}{
		{"self host on brain port", "http://10.0.0.5:9019/api/state", true},
		{"unspecified v4 on brain port", "http://0.0.0.0:9019/", true},
		{"unspecified v6 on brain port", "http://[::]:9019/", true},
		// a DIFFERENT host, and the same host on a DIFFERENT port, stay allowed
		{"self host other port", "http://10.0.0.5:3000/", false},
		{"other private host", "http://app.internal:3000/", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := guardNavigateURL(c.url, brain)
			if c.blocked && err == nil {
				t.Errorf("expected %q to be BLOCKED, but it was allowed", c.url)
			}
			if !c.blocked && err != nil {
				t.Errorf("expected %q to be ALLOWED, but it was blocked: %v", c.url, err)
			}
		})
	}
}

func TestBrowserToolsRegistration(t *testing.T) {
	dir := t.TempDir()

	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cstore.Close()

	qstore, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer qstore.Close()

	artstore, err := taskartifacts.Open(filepath.Join(dir, "art.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer artstore.Close()

	bm := NewBrowserManager("127.0.0.1:9019")
	defer bm.Close()

	ws := NewWorkerSessions()
	srv := NewServer(cstore, nil, Options{
		Queue:          qstore,
		TaskArtifacts:  artstore,
		Browser:        bm,
		WorkerSessions: ws,
	})

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx := context.Background()
	cl := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	foundNavigate := false
	foundScreenshot := false
	foundClick := false
	foundInput := false
	foundGetHTML := false
	for _, tool := range res.Tools {
		switch tool.Name {
		case "browser_navigate":
			foundNavigate = true
		case "browser_screenshot":
			foundScreenshot = true
		case "browser_click":
			foundClick = true
		case "browser_input":
			foundInput = true
		case "browser_get_html":
			foundGetHTML = true
		}
	}

	if !foundNavigate {
		t.Error("expected browser_navigate tool to be registered")
	}
	if !foundScreenshot {
		t.Error("expected browser_screenshot tool to be registered")
	}
	if !foundClick {
		t.Error("expected browser_click tool to be registered")
	}
	if !foundInput {
		t.Error("expected browser_input tool to be registered")
	}
	if !foundGetHTML {
		t.Error("expected browser_get_html tool to be registered")
	}
}

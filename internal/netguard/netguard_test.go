// SPDX-License-Identifier: Elastic-2.0

package netguard

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnsafeIP(t *testing.T) {
	cases := []struct {
		ip     string
		unsafe bool
	}{
		{"169.254.169.254", true}, {"127.0.0.1", true}, {"10.0.0.5", true},
		{"192.168.1.1", true}, {"::1", true}, {"fe80::1", true}, {"0.0.0.0", true},
		{"8.8.8.8", false}, {"1.1.1.1", false}, {"93.184.216.34", false},
	}
	for _, c := range cases {
		if got := UnsafeIP(net.ParseIP(c.ip)); got != c.unsafe {
			t.Errorf("UnsafeIP(%s)=%v want %v", c.ip, got, c.unsafe)
		}
	}
}

func TestDialContext_BlocksLoopbackByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := NewGuard(nil)
	conn, err := g.DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatalf("expected DialContext to reject loopback target, got nil error")
	}
}

func TestDialContext_AllowsAllowlistedHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, _, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	g := NewGuard([]string{host})
	conn, err := g.DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("expected DialContext to allow allowlisted host, got error: %v", err)
	}
	conn.Close()
}

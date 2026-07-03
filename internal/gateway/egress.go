// SPDX-License-Identifier: Elastic-2.0

package gateway

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// Guard defends the brain against SSRF when it dials user-registered upstreams.
// By default it blocks loopback / private (RFC1918, ULA) / link-local (incl. the
// cloud metadata address 169.254.169.254) / unspecified / multicast addresses.
// An explicit host allowlist lets an admin sanction specific internal targets.
//
// It enforces at DIAL time and pins the validated IP, so a hostname can't pass a
// check and then DNS-rebind to a private IP before the connection is made; the
// same applies to redirect targets (every dial is re-validated).
type Guard struct {
	allow map[string]bool // hostnames exempt from the private-IP block
}

// NewGuard builds a guard; allowHosts (hostnames or IP literals) bypass the block.
func NewGuard(allowHosts []string) *Guard {
	m := map[string]bool{}
	for _, h := range allowHosts {
		if h = strings.TrimSpace(strings.ToLower(h)); h != "" {
			m[h] = true
		}
	}
	return &Guard{allow: m}
}

// AllowCount reports the number of allowlisted hosts (for logging).
func (g *Guard) AllowCount() int {
	if g == nil {
		return 0
	}
	return len(g.allow)
}

func unsafeIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast()
}

// DialContext resolves the host, rejects private/loopback/link-local targets
// (unless the host is allowlisted), and dials the validated IP directly.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	allowed := g != nil && g.allow[strings.ToLower(host)]
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var dialIP net.IP
	for _, ipa := range ips {
		if allowed || !unsafeIP(ipa.IP) {
			dialIP = ipa.IP
			break
		}
	}
	if dialIP == nil {
		return nil, fmt.Errorf("egress blocked by SSRF guard: %s resolves only to private/loopback/link-local addresses", host)
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(dialIP.String(), port)) // pin the validated IP
}

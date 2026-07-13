// SPDX-License-Identifier: Elastic-2.0

// Package netguard provides the resolve-and-pin SSRF guard shared by the
// gateway, browser, and forge egress paths.
package netguard

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// Guard defends against SSRF when dialing user-registered upstreams. By
// default it blocks loopback / private (RFC1918, ULA) / link-local (incl. the
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

// imdsLiterals is the CANONICAL cloud instance-metadata (IMDS) IP-literal set:
// the addresses not otherwise caught by the private/link-local checks. The
// Alibaba IMDS 100.100.100.100 is a PUBLIC 100.64/10 (CGNAT) literal — Go's
// net.IP.IsPrivate() does NOT cover 100.64/10, so without this it would dial.
// The AWS IPv6 IMDS fd00:ec2::254 is a unique-local address already flagged by
// IsPrivate(); it is listed for clarity/defence-in-depth. The well-known
// 169.254.169.254 (AWS/GCP/Azure/ECS) is deliberately NOT here — it is already a
// link-local address, so both consumers catch it via their link-local predicate.
//
// This is the single source of truth: netguard.UnsafeIP consults it below, and
// internal/brain's browser predicate consults it via IsIMDSLiteral (there is no
// second, divergent copy).
var imdsLiterals = []net.IP{
	net.ParseIP("100.100.100.100"), // Alibaba Cloud IMDS
	net.ParseIP("fd00:ec2::254"),   // AWS IPv6 IMDS
}

// IsIMDSLiteral reports whether ip is one of the canonical cloud-IMDS IP literals
// (Alibaba 100.64/10, AWS IPv6 ULA) not caught by the standard private/link-local
// predicates. It is the shared source both netguard and the agent browser consult
// so the two never drift out of sync.
func IsIMDSLiteral(ip net.IP) bool {
	for _, imds := range imdsLiterals {
		if imds != nil && imds.Equal(ip) {
			return true
		}
	}
	return false
}

// UnsafeIP reports whether ip is loopback, private, unspecified, link-local
// (unicast or multicast), interface-local multicast, otherwise multicast, or a
// known cloud-metadata (IMDS) literal not covered by those (Alibaba 100.64/10).
func UnsafeIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return true
	}
	return IsIMDSLiteral(ip)
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
		if allowed || !UnsafeIP(ipa.IP) {
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

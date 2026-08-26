// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/advpool"
)

// localEndpointRoles are the seats a --local-endpoint may place. They are the
// canonical role keys the rest of the pool already uses, not a parallel
// vocabulary: an operator who has read any other corral output already knows
// these names.
var localEndpointRoles = []string{
	advpool.RoleMutantGenerator,
	advpool.RoleTestWriter,
	advpool.RoleTestCritic,
	advpool.RoleMutantGeneratorShadow,
	advpool.RoleTestWriterShadow,
}

// parseLocalEndpoints turns repeated `<role>=<url>` values into a role→URL map.
//
// WHY THIS EXISTS. A local model runs wherever its ollama daemon runs, and a
// daemon is pinned to a device by the environment it starts in
// (HIP_VISIBLE_DEVICES / ROCR_VISIBLE_DEVICES / CUDA_VISIBLE_DEVICES). corral
// therefore cannot — and must not try to — select a GPU: it selects the
// DAEMON, and the operator pins each daemon to a card. Two daemons on two
// ports is how two models sit on two cards concurrently, instead of one card
// evicting one model to load the other.
//
// Without this, OLLAMA_URL is process-wide (four resolution sites) and every
// local seat in a run shares one daemon, one card, and one VRAM budget.
//
// Every failure here is REFUSED rather than defaulted. A mistyped role that
// silently placed a seat nowhere would look exactly like a seat that ran on
// the intended card, and the operator would compare two numbers produced on
// one device believing they came from two.
func parseLocalEndpoints(vals []string) (map[string]string, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	known := map[string]bool{}
	for _, r := range localEndpointRoles {
		known[r] = true
	}
	out := map[string]string{}
	for _, v := range vals {
		role, raw, ok := strings.Cut(v, "=")
		if !ok {
			return nil, fmt.Errorf("--local-endpoint %q: want <role>=<url>, e.g. %s=http://localhost:11435", v, advpool.RoleTestWriter)
		}
		role = strings.TrimSpace(role)
		raw = strings.TrimSpace(raw)
		if !known[role] {
			return nil, fmt.Errorf("--local-endpoint %q: unknown role %q; known roles are %s", v, role, strings.Join(sortedRoles(), ", "))
		}
		if _, dup := out[role]; dup {
			return nil, fmt.Errorf("--local-endpoint: role %q given twice; one endpoint per role", role)
		}
		if raw == "" {
			return nil, fmt.Errorf("--local-endpoint %q: empty url", v)
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("--local-endpoint %q: %w", v, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("--local-endpoint %q: need an absolute url with a scheme and host, e.g. http://localhost:11436", v)
		}
		out[role] = strings.TrimSuffix(raw, "/")
	}
	return out, nil
}

func sortedRoles() []string {
	r := append([]string(nil), localEndpointRoles...)
	sort.Strings(r)
	return r
}

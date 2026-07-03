// SPDX-License-Identifier: Elastic-2.0

// forges.go — host-keyed forge registry, ForgesFromEnv, and Engine.providerFor.
package repo

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// ForgeConfig holds the connection configuration for one forge host.
type ForgeConfig struct {
	Type    string // "github" | "gitea" | "gitlab"
	APIBase string // REST API base, e.g. "https://api.github.com"
	Token   string // PAT / app token; may be empty for public repos
}

// ForgesFromEnv builds the host→ForgeConfig registry from environment variables.
//
// Defaults (always present, can be overridden):
//
//	github.com → github, https://api.github.com
//	gitlab.com → gitlab, https://gitlab.com/api/v4
//
// Back-compat (existing single-forge env vars):
//
//	CORRALAI_GIT_TOKEN   → token for github.com
//	CORRALAI_GITHUB_API  → apiBase for github.com (default https://api.github.com)
//
// Multi-forge (self-hosted or additional forges):
//
//	CORRALAI_FORGES = "host=type,apiBase,token;host=type,apiBase,token;..."
//	Example: "gitea.example.com=gitea,https://gitea.example.com/api/v1,mytoken"
func ForgesFromEnv() map[string]ForgeConfig {
	forges := map[string]ForgeConfig{
		"github.com": {Type: "github", APIBase: "https://api.github.com"},
		"gitlab.com": {Type: "gitlab", APIBase: "https://gitlab.com/api/v4"},
	}

	// Back-compat: single-forge env vars configure github.com.
	if tok := os.Getenv("CORRALAI_GIT_TOKEN"); tok != "" {
		cfg := forges["github.com"]
		cfg.Token = tok
		forges["github.com"] = cfg
	}
	if api := os.Getenv("CORRALAI_GITHUB_API"); api != "" {
		cfg := forges["github.com"]
		cfg.APIBase = api
		forges["github.com"] = cfg
	}

	// CORRALAI_FORGES: semicolon-separated "host=type,apiBase,token" entries.
	if raw := os.Getenv("CORRALAI_FORGES"); raw != "" {
		for _, entry := range strings.Split(raw, ";") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			idx := strings.Index(entry, "=")
			if idx <= 0 {
				continue // malformed: no "host=" prefix
			}
			host := strings.TrimSpace(entry[:idx])
			parts := strings.SplitN(strings.TrimSpace(entry[idx+1:]), ",", 3)
			if len(parts) < 3 || host == "" {
				continue // malformed: need type,apiBase,token
			}
			forges[host] = ForgeConfig{
				Type:    strings.TrimSpace(parts[0]),
				APIBase: strings.TrimSpace(parts[1]),
				Token:   strings.TrimSpace(parts[2]),
			}
		}
	}

	return forges
}

// providerFor returns the Provider for the given host using the engine's forge
// registry. Returns a clear error for unknown hosts or unimplemented types.
func (e *Engine) providerFor(host string) (Provider, error) {
	cfg, ok := e.forges[host]
	if !ok {
		return nil, fmt.Errorf("no forge configured for host %q (known hosts: %s)", host, knownHosts(e.forges))
	}
	switch cfg.Type {
	case "github":
		return &githubProvider{rc: restClient{
			base:   cfg.APIBase,
			token:  cfg.Token,
			accept: "application/vnd.github+json",
			// authScheme defaults to "Bearer" (empty = Bearer in authHeader())
		}}, nil
	case "gitea":
		// Gitea mirrors GitHub's REST shape but uses "token <tok>" auth and
		// does not require a vendor Accept header.
		return &giteaProvider{rc: restClient{
			base:       cfg.APIBase,
			token:      cfg.Token,
			accept:     "", // Gitea does not accept application/vnd.github+json
			authScheme: "token",
		}}, nil
	case "gitlab":
		// GitLab uses PRIVATE-TOKEN header (not Authorization: Bearer) and
		// the oauth2: push-cred form. No vendor Accept header.
		return &gitlabProvider{rc: restClient{
			base:    cfg.APIBase,
			token:   cfg.Token,
			authKey: "PRIVATE-TOKEN",
		}}, nil
	default:
		return nil, fmt.Errorf("unknown forge type %q for host %q", cfg.Type, host)
	}
}

// knownHosts returns a sorted, comma-separated list of host keys in the registry
// for use in error messages.
func knownHosts(m map[string]ForgeConfig) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

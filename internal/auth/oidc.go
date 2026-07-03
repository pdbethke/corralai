// SPDX-License-Identifier: Elastic-2.0

// Package auth makes the CorralAI brain a standard OIDC relying party AND enforces
// authorization on top of authentication:
//
//   - NewVerifier builds JWKS verifiers for one or more trusted clients (e.g. the
//     confidential `corral` service and the public `corral-cli` user login).
//   - Wrap turns those into the go-sdk's RequireBearerToken middleware, which puts
//     a verified TokenInfo (principal + tenant claims) into the request — reachable
//     by every tool handler. This is what makes attribution authoritative.
//   - Authorizer is a principal allowlist: a valid token is necessary but not
//     sufficient — only listed principals may use the brain (403 otherwise). This
//     is the "authentication ≠ authorization" gate that matters once public.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// delegPrefix tags a brain-minted delegation token (for out-of-process subagents)
// so VerifyToken routes it to HMAC verification instead of OIDC/JWKS.
const delegPrefix = "cdt_"

type delegClaims struct {
	P  string `json:"p"`            // principal the subagent rolls up to (for authorization)
	S  string `json:"s"`            // subagent coordination identity ("principal/child")
	E  int64  `json:"e"`            // expiry, unix seconds
	RO bool   `json:"ro,omitempty"` // read-only observer: may view the swarm, may NOT act
}

// uaTransport forces a stable custom User-Agent on JWKS/discovery fetches. Some
// IdPs sit behind a WAF or bot-fight layer (e.g. Cloudflare) that 403s the default
// Go HTTP UA, which would silently break verification; a named UA avoids that.
type uaTransport struct {
	ua   string
	base http.RoundTripper
}

func (t *uaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("User-Agent", t.ua)
	return t.base.RoundTrip(r)
}

// Pair is a trusted OIDC client: tokens must carry this Issuer and (unless empty)
// this Audience (the client_id).
type Pair struct {
	Issuer   string
	Audience string
}

// Verifier validates bearer JWTs against any of its configured providers. When no
// non-empty issuer is configured it is disabled (dev mode) and Wrap is a no-op.
type Verifier struct {
	vs         []*oidc.IDTokenVerifier
	httpClient *http.Client
	enabled    bool
	delegKey   []byte // HMAC key for subagent delegation tokens (nil => delegation off)
}

// EnableDelegation enables minting/verification of subagent delegation tokens,
// signed with key. It does NOT flip the verifier "enabled" flag: delegation rides
// on OIDC being configured (in prod, where the bearer middleware runs and verifies
// these tokens alongside JWTs). In dev (no OIDC) auth is off and a subagent token
// needs no verification, so this is just a no-op-until-prod key install.
func (vf *Verifier) EnableDelegation(key []byte) {
	if len(key) == 0 {
		return
	}
	vf.delegKey = key
}

func (vf *Verifier) sign(payload []byte) string {
	m := hmac.New(sha256.New, vf.delegKey)
	m.Write(payload)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// MintDelegation issues a TTL-bound token authenticating as subagent (identity)
// rolling up to principal (authorization). The holder can act as the subagent but
// never exceeds the principal's authz, and the token expires.
func (vf *Verifier) MintDelegation(principal, subagent string, ttl time.Duration) (string, error) {
	if len(vf.delegKey) == 0 {
		return "", fmt.Errorf("delegation not enabled")
	}
	c := delegClaims{P: principal, S: subagent, E: time.Now().Add(ttl).Unix()}
	payload, _ := json.Marshal(c)
	b := base64.RawURLEncoding.EncodeToString(payload)
	return delegPrefix + b + "." + vf.sign([]byte(b)), nil
}

// MintObserver issues a READ-ONLY token: it authenticates (passes the principal
// allowlist, so it can view /api/state + /events) but is rejected on every action
// surface (/mcp/ tools, /api/instruct). This is the credential you hand to people
// who should watch the swarm but not touch it — dashboards, demo viewers, exporters.
func (vf *Verifier) MintObserver(principal string, ttl time.Duration) (string, error) {
	if len(vf.delegKey) == 0 {
		return "", fmt.Errorf("delegation not enabled")
	}
	c := delegClaims{P: principal, S: principal + "/observer", E: time.Now().Add(ttl).Unix(), RO: true}
	payload, _ := json.Marshal(c)
	b := base64.RawURLEncoding.EncodeToString(payload)
	return delegPrefix + b + "." + vf.sign([]byte(b)), nil
}

func (vf *Verifier) verifyDelegation(token string) (*sdkauth.TokenInfo, error) {
	if len(vf.delegKey) == 0 {
		return nil, sdkauth.ErrInvalidToken
	}
	body := strings.TrimPrefix(token, delegPrefix)
	dot := strings.LastIndexByte(body, '.')
	if dot < 0 {
		return nil, sdkauth.ErrInvalidToken
	}
	b, sig := body[:dot], body[dot+1:]
	if !hmac.Equal([]byte(sig), []byte(vf.sign([]byte(b)))) {
		return nil, sdkauth.ErrInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(b)
	if err != nil {
		return nil, sdkauth.ErrInvalidToken
	}
	var c delegClaims
	if err := json.Unmarshal(raw, &c); err != nil || c.P == "" || c.S == "" {
		return nil, sdkauth.ErrInvalidToken
	}
	if time.Now().Unix() >= c.E {
		return nil, sdkauth.ErrInvalidToken
	}
	// UserID is the PRINCIPAL (so the allowlist + role checks roll up); the subagent
	// identity rides in Extra and is what coordination tools attribute work to.
	return &sdkauth.TokenInfo{
		Expiration: time.Unix(c.E, 0),
		UserID:     c.P,
		Extra: map[string]any{
			"principal": c.P,
			"email":     c.P,
			"subagent":  c.S,
			"readonly":  c.RO,
		},
	}, nil
}

// ReadOnly reports whether the request's verified bearer is a read-only observer
// token (may view, may not act). Action handlers gate on this.
func ReadOnly(r *http.Request) bool {
	if ti := sdkauth.TokenInfoFromContext(r.Context()); ti != nil && ti.Extra != nil {
		ro, _ := ti.Extra["readonly"].(bool)
		return ro
	}
	return false
}

// NewVerifier builds a verifier per non-empty Pair. Empty list => disabled (dev).
func NewVerifier(ctx context.Context, pairs []Pair) (*Verifier, error) {
	var ps []Pair
	for _, p := range pairs {
		if strings.TrimSpace(p.Issuer) != "" {
			ps = append(ps, p)
		}
	}
	if len(ps) == 0 {
		return &Verifier{enabled: false}, nil
	}
	hc := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &uaTransport{ua: "CorralAI-OIDC/1.0", base: http.DefaultTransport},
	}
	v := &Verifier{httpClient: hc, enabled: true}
	for _, p := range ps {
		provider, err := oidc.NewProvider(oidc.ClientContext(ctx, hc), p.Issuer)
		if err != nil {
			return nil, err
		}
		cfg := &oidc.Config{ClientID: p.Audience}
		if p.Audience == "" {
			cfg.SkipClientIDCheck = true
		}
		v.vs = append(v.vs, provider.Verifier(cfg))
	}
	return v, nil
}

func (vf *Verifier) Enabled() bool { return vf.enabled }
func (vf *Verifier) Count() int    { return len(vf.vs) }

// VerifyToken is a go-sdk TokenVerifier: it accepts a token if any configured
// verifier validates it, extracting the principal (email) and tenant claims into
// TokenInfo so handlers can attribute actions to the verified identity.
func (vf *Verifier) VerifyToken(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
	if strings.HasPrefix(token, delegPrefix) {
		return vf.verifyDelegation(token)
	}
	ctx = oidc.ClientContext(ctx, vf.httpClient)
	for _, v := range vf.vs {
		idt, err := v.Verify(ctx, token)
		if err != nil {
			continue
		}
		var c struct {
			Email             string `json:"email"`
			PreferredUsername string `json:"preferred_username"`
			TenantID          string `json:"tenant_id"`
			TenantRole        string `json:"tenant_role"`
		}
		_ = idt.Claims(&c)
		principal := c.Email
		if principal == "" {
			principal = c.PreferredUsername
		}
		exp := idt.Expiry
		if exp.IsZero() {
			exp = time.Now().Add(time.Hour)
		}
		return &sdkauth.TokenInfo{
			Expiration: exp,
			UserID:     principal, // also binds the MCP session to this user (anti-hijack)
			Extra: map[string]any{
				"principal":   principal,
				"email":       c.Email,
				"tenant_id":   c.TenantID,
				"tenant_role": c.TenantRole,
			},
		}, nil
	}
	return nil, sdkauth.ErrInvalidToken
}

// Wrap applies bearer-token authentication (when enabled). The verified TokenInfo
// is placed in the request context and threaded to tool handlers.
func (vf *Verifier) Wrap(next http.Handler) http.Handler {
	if !vf.enabled {
		return next
	}
	return sdkauth.RequireBearerToken(vf.VerifyToken, nil)(next)
}

// Principal returns the verified principal (email) from the bearer TokenInfo in
// ctx, or "" when unauthenticated.
func Principal(ctx context.Context) string {
	if ti := sdkauth.TokenInfoFromContext(ctx); ti != nil {
		return ti.UserID
	}
	return ""
}

// PrincipalKey returns a per-principal rate-limit key from the verified token, or
// "" when unauthenticated (so a per-principal limiter is a no-op pre-auth).
func PrincipalKey(r *http.Request) string {
	if ti := sdkauth.TokenInfoFromContext(r.Context()); ti != nil && ti.UserID != "" {
		return "user:" + ti.UserID
	}
	return ""
}

// Authorizer is a principal allowlist enforced AFTER authentication. It is backed
// either by a static set (NewAuthorizer) or by a live source such as the principal
// store (NewAuthorizerFunc), so the allowlist can change at runtime without restart.
type Authorizer struct {
	allowed map[string]bool   // static backing (NewAuthorizer)
	allowFn func(string) bool // dynamic backing (NewAuthorizerFunc)
	countFn func() int        // dynamic size (NewAuthorizerFunc)
}

// NewAuthorizer builds a static allowlist. Empty (no principals) => allow any
// authenticated caller (back-compat / single-operator dev).
func NewAuthorizer(principals []string) *Authorizer {
	m := map[string]bool{}
	for _, p := range principals {
		if p = strings.TrimSpace(p); p != "" {
			m[strings.ToLower(p)] = true
		}
	}
	return &Authorizer{allowed: m}
}

// NewAuthorizerFunc backs the allowlist with live functions (e.g. the principal
// store), so adding/removing a principal takes effect without a restart.
func NewAuthorizerFunc(allow func(string) bool, count func() int) *Authorizer {
	return &Authorizer{allowFn: allow, countFn: count}
}

func (a *Authorizer) Count() int {
	if a.countFn != nil {
		return a.countFn()
	}
	return len(a.allowed)
}

// Allowed reports whether a principal may use the brain.
func (a *Authorizer) Allowed(principal string) bool {
	if a.allowFn != nil {
		return a.allowFn(principal)
	}
	if len(a.allowed) == 0 {
		return true
	}
	return a.allowed[strings.ToLower(strings.TrimSpace(principal))]
}

// Middleware rejects (403) authenticated requests whose principal isn't allowed.
// Must run AFTER Wrap so TokenInfo is present.
func (a *Authorizer) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := ""
		if ti := sdkauth.TokenInfoFromContext(r.Context()); ti != nil {
			principal = ti.UserID
		}
		if !a.Allowed(principal) {
			http.Error(w, "forbidden: principal not authorized for this brain", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

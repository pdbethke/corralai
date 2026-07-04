<!-- SPDX-License-Identifier: Elastic-2.0 -->
# The corral pod — a server-side frontier-worker pool

The pod runs a herd of **frontier vendor workers** (Claude, Gemini, Codex,
Copilot — subscription CLIs, no API keys) as docker containers on a box that
**already runs a production brain** (`corral.service`, auth-on). Missions run
server-side, 24/7, independent of any workstation: assign work from your phone,
review the resulting PRs over coffee.

This differs from `deploy/demo` in three ways: no brain and no Ollama here (the
brain already exists; the box has no GPU), the workers are **frontier** (CPU +
network + creds only), and the brain is **auth-on** (workers carry a token).

> **Status: v1 draft.** The compose/image/proxy are written; the auth ceremony
> and worker-token mint are **finalized on the box with real credentials** —
> none of this is validated against the prod brain yet. Items marked **⚙ verify
> on the box** are the ones to confirm during setup.

## Architecture

```
host box
┌───────────────────────────────────────────────────────────────┐
│  corral.service        127.0.0.1:9019   auth-ON (WebOffice     │
│      ▲ Bearer <worker-token>            Authentik, corral-cli) │
│  ┌───┴───────────────  docker net: corralai-pod  ───────────┐  │
│  │  auth-proxy (nginx) ── injects Authorization: Bearer ──┐  │  │
│  │        ▲ tokenless http                                │  │  │
│  │  builder=Claude  tester=Gemini  pentester=Codex        │  │  │
│  │  reviewer=Copilot                 shared /workspace ───┘  │  │
│  └──────────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────────┘
```

**Why the proxy.** The prod brain refuses tokenless requests ("no bearer
token"). The four vendor CLIs each configure MCP differently and none has a
clean "add this bearer header" knob. So instead of injecting a token four ways,
one nginx sidecar holds the single worker token, and the workers connect to it
tokenless. This is the `corral-observe` pattern (a credentialed reverse proxy)
generalized from read-only to read-write.

## Auth: two planes, no new identity infra

- **Human → brain:** OIDC via the **existing** WebOffice Authentik `corral-cli`
  app (`CORRALAI_OIDC_ISSUER=…/corral-cli/`). Unchanged. Used by *you* to mint
  the worker token and to watch the UI. **No new Authentik, not Tannhäuser.**
- **Worker → brain:** the minted **bearer token** the proxy holds. No IdP.

### Mint the worker token  ⚙ verify on the box
The worker is a non-human principal with normal (non-admin) rights — it must
pass tasks but the human gate still refuses it shared-memory/skill writes.

```bash
# authenticate as yourself (OIDC device/browser flow) to get an operator token,
# then mint a scoped worker token. Confirm the exact verb — mint-observer is
# READ-ONLY and will NOT work for workers; a worker needs a read-write token.
corral-admin --brain https://brain.corralai.dev mint-worker --role builder --ttl 168h   # ⚙ verify verb
# save the printed token, root-only:
install -m600 -D /dev/stdin /etc/corral-pod/worker-token   # paste token, Ctrl-D
```

If no `mint-worker` verb exists yet, the fallback is the same bootstrap path the
container agents use (`corral-agent` gets `CORRAL_TOKEN` from a bootstrap
response) — capture one worker token that way. **This is the single most
important thing to nail down first;** nothing else in the pod works without it.

## The credential ceremony (the headless-box dance)

The OAuth CLIs (Claude, Gemini, Codex) want a localhost browser callback the box
doesn't have. Two ways through — you have done this on this box before:

1. **Auth locally, promote the dotfiles.** Log in on your workstation, then copy
   the credential dirs up into `/etc/corral-pod/creds/<vendor>/` (root, 0600):
   - Claude → `~/.claude` (the credentials file within)
   - Gemini → `~/.gemini` (holds `google_accounts.json`, the 0.13.x cred store)
   - Codex → `~/.codex` (`auth.json`)
   - Copilot → `~/.copilot` **and** `~/.config/gh` (Copilot rides gh auth)
   Refresh tokens self-renew for weeks; re-copy when a vendor starts 401ing.
2. **`ssh -L` the callback.** Forward the OAuth callback port and run the login
   *on the box*, completing it in your local browser through the tunnel.

Copilot and `gh` also support **device-code** flows (headless-native) — the two
easiest seats. This is the pod's main ongoing cost: it is an operator-maintained
**pet** with a re-blessing chore, not fire-and-forget cattle. **ToS: keep it a
single-operator setup** — these are your personal subscriptions.

## Reaching a loopback-bound brain  ⚙ verify on the box

`corral.service` binds `127.0.0.1:9019`. A container cannot reach the host's
`127.0.0.1` via the docker gateway, so `host-brain:host-gateway` (used in the
compose) resolves to the gateway IP the brain is **not** listening on. Pick one:

- **A (simplest):** add the proxy to the host network — set the `auth-proxy`
  service `network_mode: host` and `BRAIN_UPSTREAM=http://127.0.0.1:9019`. The
  proxy then shares host loopback and reaches the brain directly. (Workers stay
  on the private `pod` net and reach the proxy by service name — but with the
  proxy on host net they'd reach it via `127.0.0.1:9019` on the host too; keep
  the proxy on the pod net and instead do B.)
- **B (clean):** bind-mount the brain's unix socket if it has one, or add a
  second brain listener on the docker bridge IP (`CORRALAI_ADDR` also on
  `172.x`), firewalled to the pod net. Preserves isolation.

Resolve this before first bring-up; it is the other genuine unknown.

## Bring-up

```bash
cd deploy/pod
cp .env.example .env && $EDITOR .env          # fill creds paths + token file
docker compose build                          # fat image: 4 CLIs + harness (~minutes)
docker compose up -d
docker compose logs -f auth-proxy builder     # watch the proxy + one worker connect
```

Verify the workers registered: open the brain UI (or `curl` its `/api/state`
through an operator token) and confirm four hosts appear in the topology with
their vendor/model labels. Then launch a mission (`corral-admin … start-mission`
or the UI) and watch the herd claim tasks.

## Teardown

```bash
docker compose down                # workers stop; the brain (systemd) is untouched
```

## Known warnings (carried from Run D pre-flight)

- **Codex runs unsandboxed.** `codex exec` auto-denies MCP calls under every
  approval mode; only `--dangerously-bypass-approvals-and-sandbox` lets it work,
  which turns *its* sandbox off. The corral **bwrap jail** is then the only
  boundary for that worker — acceptable in the pod (trusted, single-operator),
  but do not widen it.
- **Model pins matter.** Pin each seat (`*_MODEL` in `.env`) so attribution is
  exact and no worker silently draws from the model/quota you use interactively
  — the Fable-exhaustion lesson that killed Run D.
- **Quota is the frontier failure mode.** A worker that exhausts its window
  stops responding while holding claims (a stall the brain's #40 lease-release +
  refusal-escalation fixes partially cover, but a dead worker files no
  refusals). Watch windows on long missions.

## What this is not (yet)

Not a productized "corral pod in your cloud" — that is a later Terraform/packer
deliverable (ticket #43). This is the operator's own pet pool on one box. It is
also the physical form of dogfooding (#25) and the team-use story (#30).

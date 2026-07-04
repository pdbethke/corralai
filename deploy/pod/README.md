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

### Mint the worker token
The worker token is a **delegation token from `spawn_subagent`** — TTL-bound,
identity-scoped, and NOT coupled to any parent process (it lives for its TTL
whether or not a parent runs; despawn is explicit). Because delegation tokens
carry `Extra["subagent"]`, the human gate correctly refuses them admin writes —
a worker can pass tasks but cannot self-vet skills/memory. (`mint-observer` is
read-only and will NOT work here.) Delegation must be enabled on the brain
(`delegation.conf` on the prod box — already set).

Authenticate as yourself (OIDC), then call `spawn_subagent` with a long TTL:

```json
{ "name": "pod-builder", "role": "builder", "out_of_process": true, "ttl_seconds": 604800 }
```

Save the returned `token` to the file the proxy mounts:

```bash
install -m600 -D /dev/stdin /etc/corral-pod/worker-token   # paste token, Ctrl-D
```

One token can serve all four workers (they share the proxy), or mint one per
role for cleaner attribution. **A thin `corral-admin mint-worker` wrapper around
`spawn_subagent` (long TTL) would make this a one-liner — worth adding; see the
floor variant below, which needs the same mint.**

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

## Floor variant: workstation workers → prod brain

The guaranteed-for-a-demo path, and the safety net for the pod. Identical on
screen (the audience sees `brain.corralai.dev` doing work; where the workers sit
is invisible), but nothing to build or auth on the box — the vendor CLIs are
already installed and logged in on your workstation.

It reuses two pieces from the pod and drops the rest: **the auth-proxy** (run it
locally, upstream `https://brain.corralai.dev`) and **the token mint** (same
`spawn_subagent` call). No docker image, no credential ceremony, no
loopback problem (the brain is public HTTPS).

```bash
# 1. build the harness once
go build -o /usr/local/bin/corral-harness ./cmd/corral-harness

# 2. mint a worker token (needs your operator auth once) → save to ./worker-token
corral-admin --brain https://brain.corralai.dev mint-worker --role builder --ttl 168h > worker-token

# 3. run the auth-proxy locally, pointed at the public brain
docker run -d --name corral-floor-proxy -p 9019:9019 \
  -v "$PWD/nginx.conf.template:/etc/nginx/templates/default.conf.template:ro" \
  -v "$PWD/worker-token:/run/worker-token:ro" \
  -e BRAIN_UPSTREAM=https://brain.corralai.dev \
  --entrypoint /bin/sh nginx:1.27-alpine \
  -c 'export WORKER_TOKEN="$(cat /run/worker-token)"; exec /docker-entrypoint.sh nginx -g "daemon off;"'
  # NOTE: proxy_pass to an https upstream needs proxy_ssl_server_name on; add it
  #       to nginx.conf.template's location block for the floor. ⚙ verify

# 4. launch the four workers on the workstation (CLIs already authed), each
#    pointing at the local proxy:
export CORRAL_BRAIN=http://localhost:9019 AGENT_WORKSPACE="$PWD/floor-ws"
mkdir -p "$AGENT_WORKSPACE"
AGENT_NAME=Claude AGENT_ROLE=builder  AGENT_MODEL=claude-opus-4-6 AGENT_BACKEND=anthropic \
  HARNESS_CMD='claude -p {prompt} --mcp-config {mcp_config} --allowedTools mcp__corral__*,Read,Write,Edit,Bash --permission-mode acceptEdits' corral-harness &
# tester=gemini, pentester=codex, reviewer=copilot — same as the pod services.
```

Then open `brain.corralai.dev`, launch a mission, watch the herd. **This is the
Saturday-afternoon floor; stand it up before betting the demo on the pod.**

## What this is not (yet)

Not a productized "corral pod in your cloud" — that is a later Terraform/packer
deliverable (ticket #43). This is the operator's own pet pool on one box. It is
also the physical form of dogfooding (#25) and the team-use story (#30).

# corral-observe — the read-only observer

A standalone, look-don't-touch window into a running corralai brain. Hand it a
**read-only observer token** and it serves the live swarm UI (agents, claims,
conflicts, the activity stream) without the ability to act. Point a wall
dashboard, an ops platform, or a demo viewer at it.

## What it is

`corral-observe` is a thin **credentialed reverse proxy**. It holds the observer
token, injects it as `Authorization: Bearer …` on every request, and forwards to
the brain's UI routes. The token lives in the observer process — never in the
browser. The brain's swarm UI ships *inside the brain*; the observer is just the
safe, credentialed window onto it.

Read-only is enforced **twice**, by design:

1. The brain `403`s any action attempted with an observer token (`/mcp` tool
   calls, `/api/instruct`).
2. The observer itself refuses any non-`GET` method before it ever reaches the
   brain — so the guarantee holds even if someone hands it a non-read-only
   token by mistake.

## 1. Mint an observer token

The easy way, from an operator (superuser) with `corral-admin`:

```sh
corral-admin mint-observer --ttl 24h        # prints the token to stdout
```

Or call the `mint_observer` tool directly from an MCP session against the brain:

```
mint_observer { "ttl_seconds": 86400 }
→ { "ok": true, "token": "cdt_…", "usage": "watch read-only with:  CORRAL_TOKEN=<token> corral-observe" }
```

`ttl_seconds` defaults to 24h. Omit `principal` to scope the token to yourself
(it must name an allowed principal so it still passes the brain's allowlist).

> Observer tokens require delegation to be enabled on the brain
> (`CORRALAI_DELEGATION_SECRET`). On a dev brain with auth disabled, the UI is
> already open on localhost and no token is needed.

## 2a. Run it locally (a binary)

```sh
go build -o corral-observe ./cmd/corral-observe

corral-observe \
  --brain https://brain.example \
  --token cdt_…observer-token… \
  --open                       # opens your browser at http://127.0.0.1:8080
```

Binds loopback (`127.0.0.1:8080`) by default. Override with `--addr` /
`CORRAL_OBSERVE_ADDR`.

## 2b. Run it as a container (for ops platforms)

```sh
CORRAL_BRAIN=https://brain.example \
CORRAL_TOKEN=cdt_…observer-token… \
docker compose -f deploy/observe/compose.yml up --build
# → http://localhost:8080
```

Or directly:

```sh
docker build -f deploy/observe/Dockerfile -t corralai-observe .
docker run --rm -p 8080:8080 \
  -e CORRAL_BRAIN=https://brain.example \
  -e CORRAL_TOKEN=cdt_…observer-token… \
  corralai-observe
```

The image is distroless and CGO-free (~a few MB). The container binds
`0.0.0.0:8080` internally; publish it with `-p`.

## Configuration

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `--brain` | `CORRAL_BRAIN` | — (required) | Brain base URL, e.g. `https://brain.example` |
| `--token` | `CORRAL_TOKEN` | — (required) | Read-only observer token |
| `--addr` | `CORRAL_OBSERVE_ADDR` | `127.0.0.1:8080` | Local listen address |
| `--open` | — | off | Open the console in your browser (local use) |
| `--ping` | — | — | Health self-check: probe `/_console/health`, exit `0`/`1` |

A flag wins over its env var. The env vars exist for containers.

## Health

`GET /_console/health` returns `200` only when the observer can actually reach
the brain's `/healthz` through the same credentialed path the UI traffic takes —
a real end-to-end check, not just "the process is up." The container
`HEALTHCHECK` runs `corral-observe -ping`, which hits that endpoint (no shell or
`curl` needed on distroless).

## The Host allowlist gotcha

The observer forwards the **brain's** hostname as the upstream `Host` header. The
brain rejects Host headers that aren't in its `CORRALAI_ALLOWED_HOSTS` allowlist
(anti DNS-rebinding). So when the brain runs behind a domain, that domain must be
listed in the brain's `CORRALAI_ALLOWED_HOSTS`. For a brain reachable as
`http://brain:9019` on a docker network, allow `brain,brain:9019`.

## Embedding in an ops platform

The console is plain HTTP on the published port — embed it in an `<iframe>`, link
to it from a dashboard, or front it with your platform's own auth. The observer
holds the only credential; downstream viewers get a read-only surface with no
token of their own.

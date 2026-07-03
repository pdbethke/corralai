# CorralAI Licensing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Relicense CorralAI from MIT to source-available (Elastic License 2.0) with a commercial dual-license path and a CLA-gated contribution flow, all landed before the repo goes public.

**Architecture:** A committed verification script (`scripts/check-licensing.sh`) is the test harness — it asserts every licensing invariant (ELv2 in LICENSE, SPDX header on every `.go` file, NOTICE present, CONTRIBUTING/CLA present, no MIT remnants). We write it first so it fails red, then make each assertion pass task-by-task. SPDX headers are applied by a second idempotent script (`scripts/add-spdx.sh`) so re-runs are safe and future files are easy to cover.

**Tech Stack:** Go 1.26.4, bash, GitHub Actions (CLA Assistant Lite via `contributor-assistant/github-action`). No new Go dependencies.

## Global Constraints

- Go module: `github.com/pdbethke/corralai`; Go version floor `go 1.26.4`.
- Public license: **Elastic License 2.0**, SPDX identifier exactly `Elastic-2.0`.
- Copyright line everywhere: `Copyright (c) 2026 Peter Bethke`.
- Commercial-licensing contact: `pdbethke@gmail.com` (swap to a `licensing@corralai.dev` alias later if desired — keep it a single real address).
- SPDX header form for Go files: the single line `// SPDX-License-Identifier: Elastic-2.0`, followed by one blank line, then the file's existing content.
- Scope of per-file headers: `.go` source only. HTML/JS/CSS embedded UI assets are covered by the repo `LICENSE`/`NOTICE`, not per-file headers.
- All work happens on branch `docs/licensing-decision` (already created). Do **not** make the repo public as part of this plan.
- The 4 build-constrained files (`internal/sandbox/isolator_linux.go`, `isolator_other.go`, `isolator_linux_test.go`, `sandbox_linux_test.go`) start with `//go:build` — the SPDX line goes *above* the build tag with a blank line between; `go build`/`go vet` in Task 3 is the guard that the constraint still parses.

---

### Task 1: Licensing verification gate + SPDX adder scripts

**Files:**
- Create: `scripts/check-licensing.sh`
- Create: `scripts/add-spdx.sh`

**Interfaces:**
- Consumes: nothing.
- Produces: `scripts/check-licensing.sh` (exit 0 = all licensing invariants hold, non-zero + diagnostics otherwise); `scripts/add-spdx.sh` (idempotently prepends the SPDX line to every `.go` file lacking it).

- [ ] **Step 1: Write the verification gate (the failing test)**

Create `scripts/check-licensing.sh`:

```bash
#!/usr/bin/env bash
# Asserts every CorralAI licensing invariant. Exit 0 = all hold.
set -uo pipefail
cd "$(dirname "$0")/.."
fail=0
note() { echo "FAIL: $1"; fail=1; }

# 1. LICENSE is Elastic License 2.0, not MIT.
grep -q 'Elastic License 2.0' LICENSE 2>/dev/null || note "LICENSE missing 'Elastic License 2.0'"
grep -qi 'MIT License' LICENSE 2>/dev/null && note "LICENSE still contains 'MIT License'"

# 2. Every .go file carries the SPDX header.
missing=$(grep -rL 'SPDX-License-Identifier: Elastic-2.0' --include='*.go' . | grep -v '/\.git/' || true)
[ -n "$missing" ] && note "Go files missing SPDX header:"$'\n'"$missing"

# 3. NOTICE exists and points to commercial licensing.
[ -f NOTICE ] || note "NOTICE missing"
grep -qi 'commercial' NOTICE 2>/dev/null || note "NOTICE missing commercial-licensing pointer"

# 4. CONTRIBUTING + CLA in place.
[ -f CONTRIBUTING.md ] || note "CONTRIBUTING.md missing"
[ -f CLA.md ] || note "CLA.md missing"
[ -f .github/workflows/cla.yml ] || note ".github/workflows/cla.yml missing"

# 5. README no longer advertises MIT.
grep -q 'Plain MIT' README.md 2>/dev/null && note "README still says 'Plain MIT'"

if [ "$fail" -eq 0 ]; then echo "OK: all licensing invariants hold"; fi
exit "$fail"
```

- [ ] **Step 2: Write the SPDX adder**

Create `scripts/add-spdx.sh`:

```bash
#!/usr/bin/env bash
# Idempotently prepend the Elastic-2.0 SPDX header to every .go file lacking it.
set -euo pipefail
cd "$(dirname "$0")/.."
HEADER='// SPDX-License-Identifier: Elastic-2.0'
find . -name '*.go' -not -path './.git/*' -print0 | while IFS= read -r -d '' f; do
  if ! grep -q 'SPDX-License-Identifier' "$f"; then
    { echo "$HEADER"; echo; cat "$f"; } > "$f.spdxtmp" && mv "$f.spdxtmp" "$f"
    echo "headered: $f"
  fi
done
```

- [ ] **Step 3: Make both executable and run the gate to verify it fails**

Run:
```bash
chmod +x scripts/check-licensing.sh scripts/add-spdx.sh
bash scripts/check-licensing.sh; echo "exit=$?"
```
Expected: multiple `FAIL:` lines (LICENSE still MIT, all 99 `.go` files missing SPDX, NOTICE/CONTRIBUTING/CLA/workflow missing, README still "Plain MIT") and `exit=1`.

- [ ] **Step 4: Commit**

```bash
git add scripts/check-licensing.sh scripts/add-spdx.sh
git commit -m "build(licensing): verification gate + SPDX adder scripts"
```

---

### Task 2: Swap LICENSE to Elastic License 2.0 + add NOTICE

**Files:**
- Modify: `LICENSE` (replace MIT text entirely)
- Create: `NOTICE`

**Interfaces:**
- Consumes: `scripts/check-licensing.sh` from Task 1.
- Produces: `LICENSE` containing verbatim ELv2 text; `NOTICE` with copyright + commercial pointer.

- [ ] **Step 1: Obtain the official ELv2 text and overwrite LICENSE**

Fetch the canonical, unmodified Elastic License 2.0 text from the official source — <https://www.elastic.co/licensing/elastic-license> (or the SPDX page <https://spdx.org/licenses/Elastic-2.0.html>) — and write it verbatim as `LICENSE`. Do **not** hand-edit the body; ELv2 is applied as-is (ownership is asserted via `NOTICE` and the SPDX headers, not by filling in names in the license body). The file must begin with the heading `Elastic License 2.0`.

- [ ] **Step 2: Create NOTICE**

Create `NOTICE`:

```
CorralAI
Copyright (c) 2026 Peter Bethke

This software is source-available under the Elastic License 2.0 (Elastic-2.0);
see the LICENSE file for terms. You may use, modify, and self-host it, but you
may not provide it to third parties as a hosted or managed service.

Commercial licensing (including hosted-service use) is available — contact
pdbethke@gmail.com.
```

- [ ] **Step 3: Run the gate to verify the LICENSE/NOTICE assertions now pass**

Run:
```bash
bash scripts/check-licensing.sh; echo "exit=$?"
```
Expected: still `exit=1`, but the LICENSE and NOTICE `FAIL:` lines are gone (remaining failures: SPDX headers, CONTRIBUTING/CLA/workflow, README).

- [ ] **Step 4: Commit**

```bash
git add LICENSE NOTICE
git commit -m "feat(licensing): adopt Elastic License 2.0 + NOTICE (off MIT)"
```

---

### Task 3: SPDX headers on every Go file

**Files:**
- Modify: all 99 `*.go` files (header prepend via script)

**Interfaces:**
- Consumes: `scripts/add-spdx.sh` and `scripts/check-licensing.sh` from Task 1.
- Produces: every `.go` file begins with `// SPDX-License-Identifier: Elastic-2.0`.

- [ ] **Step 1: Run the adder**

Run:
```bash
bash scripts/add-spdx.sh | tail -5
```
Expected: `headered: ./...` lines (one per file, ~99 total).

- [ ] **Step 2: Verify the build is intact (guards the 4 build-constrained files)**

Run:
```bash
go build ./... && go vet ./... && echo BUILD_OK
```
Expected: `BUILD_OK` with no errors. (A misplaced SPDX line above a `//go:build` tag would break compilation or trip `go vet`'s build-tag check — this is the test that it didn't.)

- [ ] **Step 3: Verify headers on the build-constrained files specifically**

Run:
```bash
head -4 internal/sandbox/isolator_linux.go internal/sandbox/isolator_other.go
```
Expected: each starts with `// SPDX-License-Identifier: Elastic-2.0`, a blank line, then its `//go:build` constraint.

- [ ] **Step 4: Run the gate to confirm the SPDX assertion passes**

Run:
```bash
bash scripts/check-licensing.sh; echo "exit=$?"
```
Expected: still `exit=1`, but no SPDX `FAIL:` lines remain (only CONTRIBUTING/CLA/workflow and README left).

- [ ] **Step 5: Run the test suite to confirm nothing regressed**

Run:
```bash
go test ./... 2>&1 | tail -15
```
Expected: all packages `ok` or `no test files` — same as before the headers.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "chore(licensing): SPDX Elastic-2.0 headers on all Go files"
```

---

### Task 4: Contribution gate — CONTRIBUTING + CLA + CLA Assistant Lite

**Files:**
- Create: `CONTRIBUTING.md`
- Create: `CLA.md`
- Create: `.github/workflows/cla.yml`

**Interfaces:**
- Consumes: `scripts/check-licensing.sh` from Task 1.
- Produces: a CLA document, a contributor guide pointing to it, and a PR-time bot that records signatures so all contributions can flow into both the ELv2 and commercial builds.

> **Note for the engineer:** `CLA.md` below is a concrete, complete short-form individual CLA granting the maintainer a relicensing-capable license (the mechanism that makes dual-licensing possible). Have a lawyer review it before the repo goes public, but it is not a placeholder — it is usable as written.

- [ ] **Step 1: Create CLA.md**

Create `CLA.md`:

```markdown
# CorralAI Individual Contributor License Agreement

Thank you for contributing to CorralAI ("the Project"), maintained by Peter
Bethke ("the Maintainer"). To keep the Project sustainable under its
source-available + commercial dual-license model, the Maintainer needs the
right to license contributions under both the Elastic License 2.0 and the
Project's commercial license. This Agreement sets out those terms. You retain
ownership of your contributions.

By signing, you agree to the following for all past and future Contributions you
submit to the Project:

1. **Definitions.** "Contribution" means any original work of authorship,
   including modifications, that you intentionally submit to the Project (e.g.
   via pull request).

2. **Copyright license.** You grant the Maintainer a perpetual, worldwide,
   non-exclusive, royalty-free, irrevocable copyright license to reproduce,
   prepare derivative works of, publicly display, publicly perform,
   sublicense, **relicense**, and distribute your Contributions and derivative
   works — including under licenses that differ from the Project's then-current
   public license (for example, a commercial license).

3. **Patent license.** You grant the Maintainer and recipients of the software a
   perpetual, worldwide, non-exclusive, royalty-free, irrevocable (except as
   stated below) patent license to make, use, sell, offer to sell, import, and
   otherwise transfer your Contribution, covering claims you can license that are
   necessarily infringed by your Contribution alone or by its combination with
   the Project. If any entity brings patent litigation alleging that the Project
   or a Contribution infringes a patent, the patent licenses you granted for that
   Contribution terminate.

4. **You are entitled to grant this.** Each Contribution is your original
   creation and you have the right to grant the licenses above. If your employer
   has rights to work you create, you have permission to make the Contribution on
   their behalf, or your employer has waived such rights.

5. **No warranty.** Contributions are provided "as is," without warranty of any
   kind.

You retain all right, title, and interest in your Contributions. Nothing here
restricts your own use of your Contributions.
```

- [ ] **Step 2: Create CONTRIBUTING.md**

Create `CONTRIBUTING.md`:

```markdown
# Contributing to CorralAI

CorralAI is **source-available** under the [Elastic License 2.0](LICENSE):
read it, modify it, self-host it. The one thing you can't do is offer it to
third parties as a hosted or managed service — that path is available under a
commercial license (contact pdbethke@gmail.com).

## Contributor License Agreement

CorralAI runs a dual-license model (ELv2 for everyone + a commercial license for
hosted-service use). For contributions to flow into both, we need a one-time
**Contributor License Agreement** from each contributor. You keep ownership of
your work; you grant the maintainer the right to license it under both.

The first time you open a pull request, a bot will ask you to sign by commenting:

> I have read the CLA Document and I hereby sign the CLA

The full text is in [CLA.md](CLA.md). It's a one-time signature covering all your
future contributions.

## Workflow

1. Open an issue or discussion for non-trivial changes first.
2. `go build ./...`, `go vet ./...`, and `go test ./...` must pass.
3. New `.go` files must carry the SPDX header — run `bash scripts/add-spdx.sh`.
4. `bash scripts/check-licensing.sh` must exit 0.
```

- [ ] **Step 3: Create the CLA Assistant Lite workflow**

Create `.github/workflows/cla.yml`:

```yaml
name: "CLA Assistant"
on:
  issue_comment:
    types: [created]
  pull_request_target:
    types: [opened, closed, synchronize]

permissions:
  actions: write
  contents: write
  pull-requests: write
  statuses: write

jobs:
  CLAAssistant:
    runs-on: ubuntu-latest
    steps:
      - name: "CLA Assistant"
        if: (github.event.comment.body == 'recheck' || github.event.comment.body == 'I have read the CLA Document and I hereby sign the CLA') || github.event_name == 'pull_request_target'
        uses: contributor-assistant/github-action@v2.6.1
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        with:
          path-to-signatures: 'signatures/cla.json'
          path-to-document: 'https://github.com/pdbethke/corralai/blob/main/CLA.md'
          branch: 'main'
          allowlist: 'pdbethke,bot*'
```

- [ ] **Step 4: Validate the workflow YAML parses**

Run:
```bash
python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/cla.yml')); print('YAML_OK')"
```
Expected: `YAML_OK`.

- [ ] **Step 5: Run the gate**

Run:
```bash
bash scripts/check-licensing.sh; echo "exit=$?"
```
Expected: still `exit=1`, but the CONTRIBUTING/CLA/workflow `FAIL:` lines are gone (only the README `Plain MIT` failure remains).

- [ ] **Step 6: Commit**

```bash
git add CONTRIBUTING.md CLA.md .github/workflows/cla.yml
git commit -m "feat(licensing): CLA + CONTRIBUTING + CLA Assistant Lite gate"
```

---

### Task 5: README rewrite + final green gate

**Files:**
- Modify: `README.md:131-139` (the Credits/License tail)

**Interfaces:**
- Consumes: everything from Tasks 1–4; `scripts/check-licensing.sh`.
- Produces: a README License section that tells the ELv2 + commercial story and removes the MIT claim; the full gate passes green.

- [ ] **Step 1: Replace the License section**

In `README.md`, replace the existing License section:

```markdown
## License

MIT — see [LICENSE](LICENSE). Plain MIT, no riders.
```

with:

```markdown
## License

CorralAI is **source-available** under the [Elastic License 2.0](LICENSE)
(`Elastic-2.0`). You're encouraged to read the whole codebase, modify it, and
self-host it. The one restriction that matters: you may **not** provide CorralAI
to third parties as a hosted or managed service.

Want to run it as a service anyway? A **commercial license** is available —
contact pdbethke@gmail.com.

Contributions are welcome under a one-time [CLA](CLA.md); see
[CONTRIBUTING.md](CONTRIBUTING.md).
```

- [ ] **Step 2: Run the full gate — expect green**

Run:
```bash
bash scripts/check-licensing.sh; echo "exit=$?"
```
Expected: `OK: all licensing invariants hold` and `exit=0`.

- [ ] **Step 3: Final build + test sanity**

Run:
```bash
go build ./... && go test ./... 2>&1 | tail -5 && echo ALL_OK
```
Expected: `ALL_OK`, all packages pass.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs(licensing): README License section — ELv2 + commercial + CLA"
```

---

## Post-plan (manual, not code)

These are operator steps for when you actually go public — listed so they aren't forgotten, not part of task execution:

1. Install/enable the **CLA Assistant Lite** action on the GitHub repo (the workflow self-bootstraps the `signatures/cla.json` store on the first PR; confirm the `signatures/` path is writable on `main`).
2. Have a lawyer skim `CLA.md` and the ELv2 application before flipping the repo public.
3. Make the repo public **only after** `bash scripts/check-licensing.sh` exits 0 on `main`.
4. Draft the actual commercial agreement when the first prospective licensee appears (deferred per the spec).

## Self-Review

- **Spec coverage:** ELv2 public license → Task 2. Commercial dual-license pointer → Tasks 2 (NOTICE), 4 (CONTRIBUTING), 5 (README). No time-bomb → ELv2 used as-is, no change date anywhere. CLA + bot → Task 4. SPDX headers → Task 3. LICENSE/NOTICE/CONTRIBUTING/README artifacts → Tasks 2,4,5. Single-pre-public-commit sequencing → handled as a branch with the public flip deferred to Post-plan. Out-of-scope items (commercial text, trademark) → left out, noted. All covered.
- **Placeholder scan:** No TBD/TODO; CLA.md and all configs are complete concrete text; the one "have a lawyer review" note is an operator caveat, not a missing step.
- **Type consistency:** Script names (`scripts/check-licensing.sh`, `scripts/add-spdx.sh`), SPDX string (`SPDX-License-Identifier: Elastic-2.0`), and the gate's assertions are referenced identically across all tasks.

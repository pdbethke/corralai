# Task 9 report: say what exists

Wrote: a CHANGELOG Added entry (tiers, gates, new fields, cost basis, redaction and no digest, real-run values); one README sentence linking docs/corral/record-fields.md; AGENTS.md: printed_metrics_test.go row requirement under the gates, and a "standing answer" paragraph naming the two gates under the recurring bug shape. record-fields.md needed no regeneration (--check passes).

Demo: `corral-rc demo --writer-model codex --mutant-model claude-code:claude-sonnet-5-5 --critic-model claude-code:claude-haiku-5-5` (exit 0, 166s). Entry: ~/.local/state/corral/ledger/scans/20261031T193745Z-nocommit-4fd13a7c0beb.json.gz (bundle.Scan, Files[0], Calls).

Values copied: Scan.TestCommand "go test ./...", Runner "go-test", Settings (json in CHANGELOG), Toolchains "not read: certify --local ...", LoadStart 4.8, LoadEnd/LoadPeak 14.57, MemAvailableStartBytes 72234979328, PeakRSSBytes 344084480, CPUMillis 9122; Files[0] MutantsUnplaced/Unmeasured/Duplicate 0, WriterAttempts {"min":1,"median":1,"max":1,"seats_ungraded":0}; Calls[*].CostBasis "agent seat: billing not known to corral" (CostMicroUSD absent).
No example given for UnitPass, Inventory, ShadowDraw, MaxTokensReached, HostNotMeasured (absent in the --local entry), nor a priced API model.

Files: CHANGELOG.md, README.md, AGENTS.md. All gates green (gofmt, vet, go test, check-security, check-licensing, gen-language-support, gen-cli-docs, gen-record-fields).

Concerns: the changelog states UnitPass is --repo only (per brief) but no --repo run was done (brief step said one, R-pre4 said demo); the entry says so. The Haiku "priced by prompt length" reasoning comes from the brief, not re-verified.

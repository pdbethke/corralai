<!-- SPDX-License-Identifier: Elastic-2.0 -->

# Periodic project reviews

These are development review notes, with the reviewer, reviewed revision,
execution evidence and limitations recorded for each round. The reviews below
were interactive Codex reviews of this repository.

| Review | Reviewed revision | Findings at review time | Status checked 2026-10-19 |
| --- | --- | --- | --- |
| [2026-10-17: certification and review execution](2026-10-17.md) | `6aa4cf8f` | Two P1, two P2 | All four closed; verified in the next round |
| [2026-10-18: re-evaluation](2026-10-18.md) | `d3787ef1`, merged as `79d003a5` with an identical tree | Two P2; no new P1 found in scope | Both closed in #446; focused checks rerun on `7fefffc0` |

For each future round, add a dated report and an index row. Record the commit
or diff reviewed, any uncommitted inputs, the paths examined, and what actually
ran. Give findings stable IDs, a severity, a concrete trigger, observed and
expected behavior, impact, and reproduction evidence. Separate defects from
recommendations and unverified concerns.

Keep the original observations when a finding is fixed or refuted. Append a
dated resolution with the fixing commit and the check that established it.
An implementation inspection alone is not an executed resolution check.
Carry unresolved IDs into the next review rather than assigning them new IDs.
Record skipped checks and omitted workflows alongside passing checks; a scoped
review finding no P1 is not a claim that the whole project has none.


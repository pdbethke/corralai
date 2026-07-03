// SPDX-License-Identifier: Elastic-2.0

package fleet

import "strings"

// ReportingSchema describes the queryable fleet reporting schema for the oracle's SQL-
// generating LLM. Derived from tableSpecs so it can't drift from what Sync actually writes.
// Only curated metadata tables/columns appear — no content stores, by construction.
func ReportingSchema() string {
	var b strings.Builder
	b.WriteString("Fleet reporting schema (read-only). Every row has a `brain` column = the swarm that reported it.\n")
	b.WriteString("Use `fleet_missions_current` for current mission state (it is fleet_missions deduped to the latest version per brain+id).\n")
	b.WriteString("Base tables (fleet_actions, fleet_missions, fleet_tasks, fleet_telemetry) live in the `remote` catalog — always prefix them with `remote.` (e.g. `SELECT ... FROM remote.fleet_actions`). fleet_missions_current is an unqualified TEMP view. fleet_actions/fleet_tasks/fleet_telemetry are append-only streams.\n\n")
	for _, ts := range tableSpecs {
		b.WriteString("TABLE remote." + ts.remote + " (brain, " + ts.cols + ")\n")
	}
	b.WriteString("VIEW fleet_missions_current — current-state (latest per brain,id) of fleet_missions\n")
	return b.String()
}

// CurrentStateViews are TEMP views the oracle creates (after attaching md:, before the
// lockdown) so the LLM can query current state without an O(N^2) correlated subquery.
// fleet_missions is append-temporal (a row per updated_ts change); the view dedups to
// the latest version per (brain, id).
func CurrentStateViews() []string {
	return []string{
		`CREATE TEMP VIEW fleet_missions_current AS
		 SELECT * FROM remote.fleet_missions
		 QUALIFY row_number() OVER (PARTITION BY brain, id ORDER BY updated_ts DESC) = 1`,
	}
}

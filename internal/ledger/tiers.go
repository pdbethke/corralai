// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"fmt"
	"sort"
	"strings"
)

// Tier says what a recorded field can be trusted FOR. The ledger's signature
// proves integrity and authorship of the record, not truth of its contents;
// the tier is the part a reader needs to know before leaning on a value.
//
// This table is the one source of the labels. A view prints a field's Note
// beside its value, and docs/corral/record-fields.md is rendered from it, so a
// label cannot be written by hand on one surface and forgotten on another.
// TestEveryRecordedFieldHasATier walks the entry's types by reflection and
// fails on any exported field without a row here: adding a field to the record
// without deciding its tier does not compile into a green build.
//
// Rows key on the Go TYPE NAME (not the import path) so a view holding a
// ledger.Row or a reviewrecord.Review asks the same way; the test refuses two
// distinct types that share a name, which would make the key ambiguous.
type Tier string

const (
	// TierMeasured: what corral itself set, observed or executed: counts,
	// durations, timestamps, settings, versions, the model and seat each role ran
	// on, and the outcome of anything it ran.
	TierMeasured Tier = "measured"
	// TierDeclared: what the audited commit or a person says (a commit's
	// author, committer, Co-authored-by and footer tools; a person's
	// adjudication, retraction or signer-change reason). Forgeable by whoever
	// wrote it, so read a commit's names as a metric about a change, never as
	// evidence of who or what wrote it, and a person's words as theirs.
	TierDeclared Tier = "declared"
	// TierMeta: the run's narrative and notes about itself (its event kinds,
	// actors, subjects and detail, its free-text notes) and a model's opinion.
	// Signed, but forgeable by the key
	// holder, and never verified as true.
	TierMeta Tier = "meta"
	// TierKey: names or links and measures nothing (repo, commit, ids, hashes,
	// run URL, signatures, Prev).
	TierKey Tier = "key"
)

// FieldTier is one row of the table: Row is the Go type name, Field the Go
// field name. Note is the label a view prints; it is required for declared
// and meta rows, because those are the ones a reader must not over-trust.
type FieldTier struct {
	Row, Field string
	Tier       Tier
	Note       string
}

const (
	noteDeclaredCommit = "declared by the commit: freely written or removed by its author, so a metric about the change and not evidence of who or what wrote it"
	noteRunAccount     = "the run's own account of itself: signed, but not verified as true"
	noteModelOpinion   = "a model's opinion: advisory, signed but not verified as true"
	notePerson         = "declared by the person who wrote it: their words, not verified as true"
	noteAdjudicator    = "declared by the person who adjudicated"
)

var fieldTiers = buildFieldTiers()

// buildFieldTiers expands the grouped literal below into rows and refuses a
// field listed twice, so a tier is decided in exactly one place.
func buildFieldTiers() map[[2]string]FieldTier {
	m := map[[2]string]FieldTier{}
	add := func(row string, tier Tier, note, fields string) {
		for _, f := range strings.Fields(fields) {
			k := [2]string{row, f}
			if prev, dup := m[k]; dup {
				panic(fmt.Sprintf("ledger/tiers.go: %s.%s listed twice (%s and %s)", row, f, prev.Tier, tier))
			}
			m[k] = FieldTier{Row: row, Field: f, Tier: tier, Note: note}
		}
	}

	// Entry: the envelope. Everything here names or links.
	add("Entry", TierKey, "", "Format Kind ScanUID Prev Retracts Hash KeyID Signature Sigstore File Raw")
	add("Entry", TierMeasured, "", "Pushed")
	add("Entry", TierDeclared, notePerson, "Reason")
	add("Bundle", TierMeasured, "", "SourcePushed")
	add("Link", TierKey, "", "Require ScanID StatementSHA256")
	add("Checkpoint", TierKey, "", "Entries Head Through")
	add("SignerChange", TierKey, "", "Action KeyID Name PubKey")
	add("SignerChange", TierDeclared, notePerson, "Reason")
	// A keyless pin is the owner's statement of which runs may sign: the
	// repository id names, the pairs are what the owner declares. The
	// certificate facts a pin is judged against are not recorded fields:
	// they are read from the bundle (Entry.Sigstore) on every read.
	add("KeylessPin", TierKey, "", "RepoID")
	add("PinnedWorkflow", TierDeclared, notePerson, "Workflow Ref")
	add("SignerKey", TierKey, "", "KeyID Name PubKey")
	add("Adjudication", TierKey, "", "Adjudicates")
	add("Adjudication", TierDeclared, noteAdjudicator, "By Verdict Reason")

	// Identity: read from the audited commit, so declared and nothing more.
	add("Identity", TierDeclared, noteDeclaredCommit, "Author Committer CoAuthors GeneratedWith")

	// ScanRow: one per run.
	add("ScanRow", TierKey, "", "ScanID ScanUID EntryHash Repo RepoIdentity Commit DiffBase RunURL StatementSHA256 RekorLogIndex RekorUUID")
	add("ScanRow", TierMeasured, "", `Audited Candidates TotalFiles Passed TreeDirty PreflightRan AllCandidates Top TreesRequested
		Scope Substrate SandboxBackend SandboxImage ModelSet CorralVersion EngineVersion Cores SeedSkipped UnitPass TestCommand Runner Settings Toolchains HostNotMeasured SelectionReused
		LoadStart LoadEnd LoadPeak MemAvailableStartBytes PeakRSSBytes CPUMillis
		Inventory ShadowDraw MaxTokensReached
		PushedBy Host SourcePushed StartedAt FinishedAt TotalMillis SelectionMillis ModelCalls InputTokens OutputTokens`)
	add("ScanRow", TierMeta, noteRunAccount, "PreflightNote")
	// ToolMutants is JSON text of []ToolMutantCounts; the tier gate walks
	// into it, so each field of ToolMutantCounts carries its own tier.
	add("ToolMutantCounts", TierKey, "", "Path Report")
	add("ToolMutantCounts", TierMeasured, "", "Match Units Taken Dropped DroppedBy Seed Refused")
	add("ToolMutantCounts", TierDeclared, "the tool's own claim, read from its report", "Tool Survived NoCoverage Killed CompileError NotRegraded")

	// Row: one per audited file.
	add("Row", TierKey, "", "Repo Commit Path ScanID RunURL StatementSHA256 ParentSHA256 CacheKey PriorDigest ReusedFromScanID")
	add("Row", TierMeasured, "", `Audited AuthoredFallback AuthoredLanding AuthoredPassMillis AuthoredTest AuthoredTestNotCollected
		BaselineFailed BaselineFlaky Candidates ChallengerJaccard ChallengerKappa ChallengerMutants ChallengerShared
		ChallengerSufficient ChallengerSurvivedShadow ChallengerSurvivedWriter ChallengerUnion Complexity
		CoveringTests CoveringUnits CriticIncomplete CriticMillis Decisions DecisionsProbed DevPassMillis Disposition
		Evidence GenerationMillis GoalsDerived GoalReused ImportOnly KillRate Lang MutantMillisMax MutantMillisMedian
		MutantsDistinct MutantsDistinctSurvived MutantsGraded MutantsInvalid MutantsPlanted MutantsTimedOut Passed
		PerMutant PoolMillis PoolTestUnsound ProvenMissed ProvenMutantIDs RegionsProbed RegionsTotal SelectedTests
		SelectionExcluded SelectionExcludedRed SelectionFallback SelectionMillis SelectionUnreported Status
		SuiteBaselineMillis SuiteTests Survivors Symbols SymbolsProbed TestSelection TestWriterFailed
		TimedOut TotalMillis Trees Uncovered DroppedRegions
		MaxProvenMissed MinKillRate MutantBudget MutantBudgetRule ModelsByRole SeatTools PromptShape WriterMode MutantsFrom
		PriorsApplied CacheHit PreflightState SharedDirs ComputedAt StartedAt
		MutantsUnplaced MutantsUnmeasured MutantsDuplicate WriterAttempts`)
	add("Row", TierMeta, noteRunAccount, "ConcurrencyNote Detail Reason")
	add("Row", TierMeta, "carries the run's full verdict document, critic opinion included: signed, but not verified as true", "VerdictJSON")
	add("Row", TierMeta, noteModelOpinion, "VacuousFindings")
	add("TestsPerMutantSpread", TierMeasured, "", "Min Median Max")

	// MutantRow, MutantAttemptRow: what was planted and what executing it said.
	add("MutantRow", TierKey, "", "MutantID ParentSHA256 Path Repo RunURL ScanID StatementSHA256")
	add("MutantRow", TierMeasured, "", "Code DurationMillis InvalidReason KilledBy Outcome Proven ProvenByAuthoredAlone SelectionRule Shape SpanEnd SpanStart TestsRun")
	add("MutantRow", TierMeasured, "", "GeneratorModel")
	// Source is what corral loaded the mutant as (model or tool): measured.
	// SourceTool and SourceOperator are read from the tool's report, which
	// anyone can edit, so they are the report's claims (founder, 2026-11-01);
	// SourceStatus is the tool's own verdict on the mutant.
	add("MutantRow", TierMeasured, "", "Source")
	add("MutantRow", TierDeclared, "read from the tool's report: the report's own claim about which tool and operator made this mutant", "SourceOperator SourceTool")
	add("MutantRow", TierDeclared, "the tool's own claim; Outcome is corral's measurement", "SourceStatus")
	// How a kill was confirmed, and the test a refuted first run blamed:
	// both are what corral's own confirmation runs measured.
	add("MutantRow", TierMeasured, "", "KillConfirmation UnrepeatedKilledBy")
	add("MutantAttemptRow", TierKey, "", "Commit MutantID Path Repo")
	add("MutantAttemptRow", TierMeasured, "", "Outcome Model Role Shadow")

	// ModelCallRow, SeatRow, CriticFindingRow: how the models were used.
	add("ModelCallRow", TierKey, "", "Path Repo RunURL ScanID StatementSHA256")
	add("ModelCallRow", TierMeasured, "", "CacheWriteInputTokens CachedInputTokens Calls InputTokens OutputTokens Retries UnmeteredCalls WallMillis Model Role")
	add("ModelCallRow", TierDeclared, "derived from a declared price table", "CostMicroUSD CostBasis")
	add("SeatRow", TierKey, "", "Commit Path Repo")
	add("SeatRow", TierMeasured, "", "AuthoredTests Catches MutantsPlanted MutantsSurvived Opportunities SoundTests Lang Region RegionComplexity RegionLines TestComplexity Dropped Model ModelUnresolved ParseRetries Role SeatTool Shadow ShadowDrawn Shard")
	add("SeatRow", TierMeta, noteModelOpinion, "CriticFlags")
	add("CriticFindingRow", TierKey, "", "Commit Path Repo QueueFindingID")
	add("CriticFindingRow", TierMeta, noteModelOpinion, "Adjudication Evidence Severity Source TargetTest TestFile TestSelector Scope")
	add("CriticFindingRow", TierMeasured, "", "Model ModelUnresolved SeatTool")

	// EventRow: the run's own timeline.
	add("EventRow", TierKey, "", "Path Repo RunURL ScanID StatementSHA256")
	add("EventRow", TierMeta, "the run's own account of its timeline: signed, but not verified as true", "Actor Detail Kind Subject")
	add("EventRow", TierMeasured, "", "Model DurationMillis Seq TS")

	// reviewrecord.Review and its findings.
	add("Review", TierKey, "", "Repo Commit StatementSHA256")
	add("Review", TierDeclared, noteDeclaredCommit, "Author Committer CoAuthors GeneratedWith")
	add("Review", TierMeasured, "", "Scope BytesShown Calls Coverage FilesShown Lang StartedAt Substrate Truncated ReviewerModel ReviewerModelResolved ReviewerTool VerifierModel VerifierModelResolved VerifierTool InputTokens OutputTokens UnmeteredCalls")
	add("Review", TierMeta, noteModelOpinion, "Opinion Sound VerifierNote VerifierOpinion")
	add("Finding", TierKey, "", "ID")
	add("Finding", TierMeasured, "", "Demoted ExitCode Script Stdout Tier Unrun")
	add("Finding", TierMeta, noteModelOpinion, "Claim Declared File Line Severity")
	add("Refutation", TierMeasured, "", "Model Demoted ExitCode Script Stdout Tier Unrun")
	add("Refutation", TierMeta, noteModelOpinion, "Argument Declared Verdict")
	return m
}

// TierOf answers what a field can be trusted for. row is the Go type name
// ("ScanRow", "Row", "Identity", ...); ok is false for a field with no row,
// which a reader must treat as unlabelled and never as measured.
func TierOf(row, field string) (Tier, bool) {
	ft, ok := fieldTiers[[2]string{row, field}]
	return ft.Tier, ok
}

// NoteOf is the label a view prints beside a field, "" when it has none.
func NoteOf(row, field string) string {
	return fieldTiers[[2]string{row, field}].Note
}

// FieldTiers is every row, sorted by Row then Field.
func FieldTiers() []FieldTier {
	out := make([]FieldTier, 0, len(fieldTiers))
	for _, ft := range fieldTiers {
		out = append(out, ft)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Row != out[j].Row {
			return out[i].Row < out[j].Row
		}
		return out[i].Field < out[j].Field
	})
	return out
}

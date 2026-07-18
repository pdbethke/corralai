// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/ed25519"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// cannedChatter is a fake agentworker.Chatter that always replies with the same
// content and never a tool call — enough to stand in for a role's model:
// mutant-generator/test-writer read the content verbatim (structured fast
// path), and the test-critic sees a plain reply with no report_finding call
// (so it files no findings).
type cannedChatter struct{ content string }

func (c cannedChatter) Chat(_ []agentworker.Message, _ []any) (agentworker.Message, error) {
	return agentworker.Message{Role: "assistant", Content: c.content}, nil
}

// The hermetic Go target: a password validator whose goal is "≥ 12 chars", the
// dev's own (partial) test for it, three mutants, and a pool-authored test that
// kills the one mutant the dev suite misses.
const localTargetCode = `package control

import "errors"

// ValidatePassword returns an error when pw is shorter than 12 characters.
func ValidatePassword(pw string) error {
	if len(pw) < 12 {
		return errors.New("password too short")
	}
	return nil
}
`

// The dev suite kills m1 (accepts all) and m2 (len<4) but MISSES m3 (len<11):
// its 8-char probe can't distinguish the 11-char boundary.
const localDevTest = `package control

import "testing"

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("abcd1234"); err == nil {
		t.Fatal("expected an error for an 8-char password")
	}
	if err := ValidatePassword("aVeryLongPassword123"); err != nil {
		t.Fatalf("unexpected error for a long password: %v", err)
	}
}
`

// Three seeded-violation mutants in testgen's parseable format. Each is a
// complete, compiling drop-in for validate.go that no longer satisfies the goal.
const localMutants = "===MUTATION_1===\n" +
	"package control\n\n" +
	"func ValidatePassword(pw string) error {\n\treturn nil\n}\n" +
	"===MUTATION_2===\n" +
	"package control\n\n" +
	"import \"errors\"\n\n" +
	"func ValidatePassword(pw string) error {\n\tif len(pw) < 4 {\n\t\treturn errors.New(\"password too short\")\n\t}\n\treturn nil\n}\n" +
	"===MUTATION_3===\n" +
	"package control\n\n" +
	"import \"errors\"\n\n" +
	"func ValidatePassword(pw string) error {\n\tif len(pw) < 11 {\n\t\treturn errors.New(\"password too short\")\n\t}\n\treturn nil\n}\n"

// The pool's authored test kills the survivor m3: an 11-char password must be
// rejected, which the compliant code does (11 < 12) but m3 (len<11) does not.
const localWriterTest = `package control

import "testing"

func TestPoolElevenCharsRejected(t *testing.T) {
	if err := ValidatePassword("elevenchars"); err == nil {
		t.Fatal("expected an error for an 11-char password")
	}
}
`

// TestDriveLocalRun_EndToEnd drives the FULL --local orchestration seam
// (driveLocalRun) over the REAL jail-backed Scorer/Validator and the REAL
// certify-chain Signer, with only the LLM faked: canned mutants a canned dev
// suite partly kills, a canned pool test that kills the survivor, and a canned
// critic that files nothing. It asserts the run converges to a signed verdict
// whose record independently verifies with certify.VerifyDSSE — the same
// sign/verify path `corral certify verify` runs.
//
// Skips cleanly when no sandbox jail is available (e.g. bwrap's unprivileged
// userns blocked on this host); the audit refuses to run tests unsandboxed, so
// there is nothing to exercise without a jail. It runs for real in CI / any
// host with a working jail.
func TestDriveLocalRun_EndToEnd(t *testing.T) {
	// Default to the OS's auto backend (bwrap on Linux). $CORRAL_TEST_JAIL lets a
	// dev/CI force a specific backend — e.g. CORRAL_TEST_JAIL=none with
	// AGENT_EXEC_UNSAFE_HOST=1 to exercise the real drive loop on a disposable
	// host whose `go` is unreachable inside bwrap (snap-packaged outside /usr).
	iso, err := sandbox.Resolve(sandbox.Config{
		Backend:    os.Getenv("CORRAL_TEST_JAIL"),
		UnsafeHost: os.Getenv("AGENT_EXEC_UNSAFE_HOST") == "1",
	})
	if err != nil || iso == nil {
		t.Skipf("no working sandbox jail on this host (%v) — --local refuses to run tests unsandboxed; skipping", err)
	}
	jail := adequacy.NewJail(iso, 120*time.Second)

	// A resolvable bwrap backend is not enough: bwrap only binds /usr into the
	// sandbox, so a host with a snap-packaged `go` (outside /usr, common on
	// Ubuntu dev boxes) has a working jail that still can't run `go` — the
	// scorer's compliant-code run would then "fail" for a reason unrelated to
	// the audit. Smoke-test the toolchain inside the jail and skip cleanly if
	// it can't run, mirroring internal/advpool's advPoolJailSkipUnlessGoWorks.
	if pass, rerr := jail.RunTest(context.Background(), nil, []string{"go", "version"}); rerr != nil || !pass {
		t.Skipf("go toolchain not reachable inside the jail on this host (rerr=%v pass=%v) — likely a snap-packaged go outside /usr; skipping", rerr, pass)
	}

	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "queue.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })

	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bs.Close() })

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "model-gen",
		advpool.RoleTestWriter:      "model-writer",
		advpool.RoleTestCritic:      "model-critic",
	}
	d, err := advpool.NewDriver(q, advpool.JailScorer{Jail: jail}, advpool.JailValidator{Jail: jail}, assign, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	d.Signer = advpool.CertSigner{Key: priv, Store: bs, Witness: nil}

	rs := advpool.RunSpec{
		Repo: "local", Commit: "local",
		Goal:        "passwords must be at least 12 characters",
		CodePath:    "validate.go",
		Code:        localTargetCode,
		DevTestPath: "validate_test.go",
		DevTestCode: localDevTest,
		NMutants:    3,
		Lang:        "go",
	}
	const missionID = 7
	if err := d.StartRun(missionID, rs, nil); err != nil {
		t.Fatal(err)
	}

	chatterFor := func(role string) agentworker.Chatter {
		switch role {
		case advpool.RoleMutantGenerator:
			return cannedChatter{content: localMutants}
		case advpool.RoleTestWriter:
			return cannedChatter{content: localWriterTest}
		case advpool.RoleTestCritic:
			return cannedChatter{content: "the dev tests exercise the boundary; no vacuous tests found"}
		default:
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	verdict, err := driveLocalRun(ctx, d, q, missionID, chatterFor, time.Millisecond, func(time.Duration) {}, io.Discard)
	if err != nil {
		t.Fatalf("driveLocalRun: %v", err)
	}
	if verdict == nil {
		t.Fatal("expected a converged verdict, got nil")
	}

	// The dev suite killed 2 of 3 mutants (0.667 ≥ 0.5 threshold) and the pool's
	// authored test proved the one survivor real — a certified, signed verdict.
	if verdict.Status != advpool.StatusCertified {
		t.Fatalf("Status = %q, want %q (dev kill-rate %.3f)", verdict.Status, advpool.StatusCertified, verdict.DevKillRate)
	}
	if verdict.MutantsTotal != 3 {
		t.Fatalf("MutantsTotal = %d, want 3", verdict.MutantsTotal)
	}
	if verdict.Survivors != 1 {
		t.Fatalf("Survivors = %d, want 1 (m3, the 11-char boundary)", verdict.Survivors)
	}
	if verdict.ProvenMissed != 1 {
		t.Fatalf("ProvenMissed = %d, want 1 (the pool's test kills m3)", verdict.ProvenMissed)
	}
	if verdict.RecordID <= 0 {
		t.Fatalf("RecordID = %d, want a signed record id > 0", verdict.RecordID)
	}

	// The signed record must independently verify — the same DSSE check
	// `corral certify verify` runs over the user's own public key.
	rec, found, err := bs.Get(verdict.RecordID)
	if err != nil || !found {
		t.Fatalf("bs.Get(%d): found=%v err=%v", verdict.RecordID, found, err)
	}
	sig, ok := rec["signature"].(string)
	if !ok || sig == "" {
		t.Fatalf("stored record %d missing signature", verdict.RecordID)
	}
	stmt, ok, verr := certify.VerifyDSSE([]byte(sig), pub)
	if verr != nil {
		t.Fatalf("VerifyDSSE: %v", verr)
	}
	if !ok || stmt == nil {
		t.Fatal("VerifyDSSE must succeed over the signed --local verdict record under the run's public key")
	}
}

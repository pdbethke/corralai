// SPDX-License-Identifier: Elastic-2.0

package statedir

import (
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
)

func treeHash(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		info, _ := d.Info()
		lines = append(lines, p+" "+info.Mode().String()+" "+string(b))
		return nil
	})
	sort.Strings(lines)
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return string(h[:])
}

func setupMigration(t *testing.T) (home, legacy, state string) {
	home = t.TempDir()
	legacy = filepath.Join(home, ".claude")
	state = filepath.Join(home, "st", "corral") // does not exist yet
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": state}, home)
	writeFile(t, filepath.Join(legacy, "corralai_certify_key"))
	writeFile(t, filepath.Join(legacy, "corralai_coord.sqlite3"))
	writeFile(t, filepath.Join(legacy, "corralai_coord.sqlite3-wal"))
	writeFile(t, filepath.Join(legacy, "unrelated.txt"))
	return
}

func TestMigrateMovesRowsAndSidecarsOnly(t *testing.T) {
	_, legacy, state := setupMigration(t)
	moves, conflicts, err := PlanMigration(legacy, PlanOptions{})
	if err != nil || len(conflicts) != 0 || len(moves) != 2 {
		t.Fatalf("plan: %d moves, %d conflicts, %v", len(moves), len(conflicts), err)
	}
	if _, err := ApplyMigration(moves); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"corralai_certify_key", "corralai_coord.sqlite3", "corralai_coord.sqlite3-wal"} {
		if _, err := os.Stat(filepath.Join(state, n)); err != nil {
			t.Errorf("%s not moved: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(legacy, n)); !os.IsNotExist(err) {
			t.Errorf("%s still at the legacy path", n)
		}
	}
	if _, err := os.Stat(filepath.Join(legacy, "unrelated.txt")); err != nil {
		t.Error("migrate touched a file that is not a table row")
	}
	info, _ := os.Stat(filepath.Join(state, "corralai_certify_key"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v, want 0600", info.Mode().Perm())
	}
	dinfo, _ := os.Stat(state)
	if dinfo.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode %v, want 0700", dinfo.Mode().Perm())
	}
	again, _, _ := PlanMigration(legacy, PlanOptions{})
	if len(again) != 0 {
		t.Errorf("second plan should move nothing, got %d", len(again))
	}
}

func TestMigrateCrossFilesystemCopiesVerifiesAndRemoves(t *testing.T) {
	_, legacy, state := setupMigration(t)
	old := rename
	rename = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV} }
	t.Cleanup(func() { rename = old })
	moves, _, _ := PlanMigration(legacy, PlanOptions{})
	if _, err := ApplyMigration(moves); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "corralai_certify_key")); string(b) != "x" {
		t.Fatalf("copied content %q", b)
	}
	if _, err := os.Stat(filepath.Join(legacy, "corralai_certify_key")); !os.IsNotExist(err) {
		t.Fatal("source not removed after a verified copy")
	}
}

func TestMigrateConflictTouchesNothing(t *testing.T) {
	home, legacy, state := setupMigration(t)
	writeFile(t, filepath.Join(state, "corralai_certify_key"))
	before := treeHash(t, home)
	_, conflicts, err := PlanMigration(legacy, PlanOptions{})
	if err != nil || len(conflicts) != 1 || conflicts[0].File != CertifyKey {
		t.Fatalf("want one CertifyKey conflict, got %+v, %v", conflicts, err)
	}
	if treeHash(t, home) != before {
		t.Fatal("planning changed the tree")
	}
}

func TestMigrateRefusesASymlinkedSource(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".claude")
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": filepath.Join(home, "st")}, home)
	real := filepath.Join(home, "dotfiles", "key")
	writeFile(t, real)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(legacy, "corralai_certify_key")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PlanMigration(legacy, PlanOptions{}); err == nil || !strings.Contains(err.Error(), real) {
		t.Fatalf("want a refusal naming the link target, got %v", err)
	}
}

func TestPlanChangesNothing(t *testing.T) {
	home, legacy, _ := setupMigration(t)
	before := treeHash(t, home)
	if _, _, err := PlanMigration(legacy, PlanOptions{}); err != nil {
		t.Fatal(err)
	}
	if treeHash(t, home) != before {
		t.Fatal("planning changed the tree")
	}
}

func TestMigrateStaleDestinationSidecarIsAConflict(t *testing.T) {
	_, legacy, state := setupMigration(t)
	writeFile(t, filepath.Join(state, "corralai_coord.sqlite3-wal"))
	moves, conflicts, err := PlanMigration(legacy, PlanOptions{})
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("want one sidecar conflict, got %+v, %v", conflicts, err)
	}
	for _, m := range moves {
		if m.File.Name == "corralai_coord.sqlite3" {
			t.Fatal("the database row must not move while its destination WAL is stale")
		}
	}
}

func TestMigrateCopyFailureLeavesNothingAtDestination(t *testing.T) {
	_, legacy, state := setupMigration(t)
	oldR, oldC := rename, copyData
	rename = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV} }
	copyData = func(dst io.Writer, src io.Reader) (int64, error) {
		_, _ = dst.Write([]byte("par")) // a partial write, then the failure
		return 3, errors.New("disk full")
	}
	t.Cleanup(func() { rename, copyData = oldR, oldC })
	moves, _, _ := PlanMigration(legacy, PlanOptions{})
	if _, err := ApplyMigration(moves); err == nil {
		t.Fatal("want the copy failure reported")
	}
	ents, _ := os.ReadDir(state)
	if len(ents) != 0 {
		t.Fatalf("destination must hold nothing (not even a temp), has %v", ents)
	}
	for _, n := range []string{"corralai_certify_key", "corralai_coord.sqlite3", "corralai_coord.sqlite3-wal"} {
		if b, err := os.ReadFile(filepath.Join(legacy, n)); err != nil || string(b) != "x" {
			t.Fatalf("source %s not intact: %q %v", n, b, err)
		}
	}
}

func TestMigrateCrossFilesystemKeepsMode(t *testing.T) {
	_, legacy, state := setupMigration(t)
	// 0640, not writeFile's 0600: a 0600 source could not tell "mode kept"
	// from "CreateTemp's default 0600 kept".
	for _, n := range []string{"corralai_certify_key", "corralai_coord.sqlite3", "corralai_coord.sqlite3-wal"} {
		if err := os.Chmod(filepath.Join(legacy, n), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	old := rename
	rename = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV} }
	t.Cleanup(func() { rename = old })
	moves, _, _ := PlanMigration(legacy, PlanOptions{})
	if _, err := ApplyMigration(moves); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"corralai_certify_key", "corralai_coord.sqlite3", "corralai_coord.sqlite3-wal"} {
		info, err := os.Stat(filepath.Join(state, n))
		if err != nil || info.Mode().Perm() != 0o640 {
			t.Errorf("%s: mode %v err %v, want the source's 0640", n, info.Mode().Perm(), err)
		}
	}
	if ents, _ := os.ReadDir(state); len(ents) != 3 {
		t.Errorf("stray files in the state dir: %v", ents)
	}
}

func TestMigrateMainFileMovesLastAndInterruptedRunResumes(t *testing.T) {
	_, legacy, state := setupMigration(t)
	moves, _, _ := PlanMigration(legacy, PlanOptions{})
	old := rename
	rename = func(a, b string) error {
		if strings.HasSuffix(b, "corralai_coord.sqlite3") { // the main file of the DB row
			return errors.New("injected")
		}
		return os.Rename(a, b)
	}
	done, err := ApplyMigration(moves)
	rename = old
	if err == nil {
		t.Fatal("want the injected failure")
	}
	if _, serr := os.Stat(filepath.Join(state, "corralai_coord.sqlite3")); !os.IsNotExist(serr) {
		t.Fatal("main file must not be at the destination before its side files")
	}
	if _, serr := os.Stat(filepath.Join(state, "corralai_coord.sqlite3-wal")); serr != nil {
		t.Fatal("side file should have moved first")
	}
	// The legacy-only refusal must still fire for the half-moved database.
	if _, rerr := Path(CoordDB); rerr == nil {
		t.Error("a half-moved database must still be refused")
	}
	_ = done
	// Without --resume the moved side file is unexplained: a conflict.
	if _, conflicts, err := PlanMigration(legacy, PlanOptions{}); err != nil || len(conflicts) != 1 {
		t.Fatalf("re-run without resume: want one conflict, got %+v %v", conflicts, err)
	}
	moves, conflicts, err := PlanMigration(legacy, PlanOptions{Resume: true})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("resume plan: %+v %v", conflicts, err)
	}
	if _, err := ApplyMigration(moves); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"corralai_coord.sqlite3", "corralai_coord.sqlite3-wal", "corralai_certify_key"} {
		if _, err := os.Stat(filepath.Join(state, n)); err != nil {
			t.Errorf("%s did not land: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(legacy, n)); !os.IsNotExist(err) {
			t.Errorf("%s still at the legacy path", n)
		}
	}
}

func TestMigrateReportsCompletedMovesOnLaterFailure(t *testing.T) {
	_, legacy, _ := setupMigration(t)
	moves, _, _ := PlanMigration(legacy, PlanOptions{})
	if len(moves) != 2 {
		t.Fatal("setup")
	}
	fail := moves[1].To
	old := rename
	rename = func(a, b string) error {
		if b == fail {
			return errors.New("injected")
		}
		return os.Rename(a, b)
	}
	done, err := ApplyMigration(moves)
	rename = old
	if err == nil || len(done) != 1 || done[0].To != moves[0].To {
		t.Fatalf("want exactly the first move completed, got %+v, %v", done, err)
	}
	again, _, _ := PlanMigration(legacy, PlanOptions{})
	if _, err := ApplyMigration(again); err != nil {
		t.Fatal(err)
	}
	if left, _, _ := PlanMigration(legacy, PlanOptions{}); len(left) != 0 {
		t.Fatalf("re-run left %d moves", len(left))
	}
}

func TestMigrateProbeErrorFailsClosed(t *testing.T) {
	_, legacy, _ := setupMigration(t)
	old := probe
	probe = func(string) (bool, error) { return false, errors.New("EIO") }
	t.Cleanup(func() { probe = old })
	_, _, err := PlanMigration(legacy, PlanOptions{})
	if err == nil || !strings.Contains(err.Error(), "EIO") || !strings.Contains(err.Error(), "corralai_") {
		t.Fatalf("want a refusal naming the path and the error, got %v", err)
	}
}

// A side file at the destination whose main file is still at the source is
// either an interrupted earlier run (side files move first) or a stale file
// from something else. Adopting it silently replays a stranger's WAL into the
// database; it is a conflict unless the operator says this IS a resume.
func TestDestinationOnlySidecarIsAConflictUnlessResuming(t *testing.T) {
	_, legacy, state := setupMigration(t)
	if err := os.Remove(filepath.Join(legacy, "corralai_coord.sqlite3-wal")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(state, "corralai_coord.sqlite3-wal"))

	moves, conflicts, err := PlanMigration(legacy, PlanOptions{})
	if err != nil || len(conflicts) != 1 || !strings.HasSuffix(conflicts[0].To, "corralai_coord.sqlite3-wal") {
		t.Fatalf("want one conflict on the destination-only WAL, got %+v, %v", conflicts, err)
	}
	for _, m := range moves {
		if m.File == CoordDB {
			t.Fatal("the database must not move onto an unexplained WAL")
		}
	}

	moves, conflicts, err = PlanMigration(legacy, PlanOptions{Resume: true})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("resume: %+v, %v", conflicts, err)
	}
	var adopted []string
	for _, m := range moves {
		if m.File == CoordDB {
			adopted = m.Adopted
		}
	}
	if len(adopted) != 1 || adopted[0] != filepath.Join(state, "corralai_coord.sqlite3-wal") {
		t.Fatalf("resume must list the adopted side file, got %v", adopted)
	}
}

// A crash inside copyVerifyRemove can leave a .migrate-* temp — a copy of a
// private key — in a destination directory. The plan names it rather than
// leave it forever.
func TestStrayMigrateTempIsAConflict(t *testing.T) {
	_, legacy, state := setupMigration(t)
	stray := filepath.Join(state, ".migrate-123456")
	writeFile(t, stray)
	_, conflicts, err := PlanMigration(legacy, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range conflicts {
		if c.To == stray {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a conflict naming %s, got %+v", stray, conflicts)
	}
}

// Run as the wrong user (sudo, a service account), migrate would leave the
// owner's files behind and the owner's next run would mint a fresh key.
func TestMigrateRefusesASourceOwnedByAnotherUser(t *testing.T) {
	if !ownershipChecked {
		t.Skip("ownership is a unix property")
	}
	_, legacy, _ := setupMigration(t)
	old := euid
	euid = func() int { return os.Geteuid() + 1 }
	t.Cleanup(func() { euid = old })
	_, _, err := PlanMigration(legacy, PlanOptions{})
	if err == nil || !strings.Contains(err.Error(), "owned by uid") || !strings.Contains(err.Error(), "as the user who owns") {
		t.Fatalf("want a refusal naming both uids, got %v", err)
	}
}

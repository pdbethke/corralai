// SPDX-License-Identifier: Elastic-2.0

package egress

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan_PlantedAWSKeyBlocks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.go", "package config\n\nconst key = \"AKIAABCDEFGHIJKLMNOP\"\n")

	findings := Scan(context.Background(), dir, []string{"config.go"})
	if len(findings) == 0 {
		t.Fatal("expected a finding for the planted AWS key, got none")
	}
	found := false
	for _, f := range findings {
		if f.Severity == SeverityBlock && f.Path == "config.go" {
			found = true
			if f.Sample == "" {
				t.Error("finding must carry a (redacted) sample")
			}
			// The raw secret must never appear in the finding output.
			if wantAbsent := "AKIAABCDEFGHIJKLMNOP"; contains(f.Sample, wantAbsent) {
				t.Errorf("finding leaked the raw secret: %q", f.Sample)
			}
		}
	}
	if !found {
		t.Fatalf("expected a blocking finding on config.go, got: %+v", findings)
	}
}

func TestScan_PlantedPrivateKeyBlocks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "id_rsa", "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----\n")

	findings := Scan(context.Background(), dir, []string{"id_rsa"})
	blocking := 0
	for _, f := range findings {
		if f.Severity == SeverityBlock {
			blocking++
		}
	}
	if blocking == 0 {
		t.Fatalf("expected a blocking finding for the planted private key, got: %+v", findings)
	}
}

func TestScan_CleanChangeSetPasses(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n")
	writeFile(t, dir, "README.md", "# hello world\n\nThis is a clean change.\n")

	findings := Scan(context.Background(), dir, []string{"main.go", "README.md"})
	for _, f := range findings {
		if f.Severity == SeverityBlock {
			t.Fatalf("expected no blocking findings on a clean change set, got: %+v", f)
		}
	}
}

func TestScan_OnlyScansChangedFiles(t *testing.T) {
	dir := t.TempDir()
	// A secret sits in a file that is NOT part of the changed set — it must be
	// ignored. Only files explicitly passed in are scanned.
	writeFile(t, dir, "unrelated.go", "package x\n\nconst k = \"AKIAABCDEFGHIJKLMNOP\"\n")
	writeFile(t, dir, "clean.go", "package x\n\nfunc F() {}\n")

	findings := Scan(context.Background(), dir, []string{"clean.go"})
	for _, f := range findings {
		if f.Path == "unrelated.go" {
			t.Fatalf("scan touched a file outside the changed set: %+v", f)
		}
	}
}

func TestScan_LicenseAdvisory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "vendor/thing/LICENSE", "GNU GENERAL PUBLIC LICENSE\nVersion 3\n")

	findings := Scan(context.Background(), dir, []string{"vendor/thing/LICENSE"})
	found := false
	for _, f := range findings {
		if f.Severity == SeverityAdvisory && f.Path == "vendor/thing/LICENSE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an advisory finding for the GPL license file, got: %+v", findings)
	}
}

func TestScan_MissingFileSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	// deleted-in-diff file: does not exist on disk. Must not panic or error.
	findings := Scan(context.Background(), dir, []string{"gone.go"})
	if len(findings) != 0 {
		t.Fatalf("expected no findings for a missing file, got: %+v", findings)
	}
}

func TestScanSecrets_OverSizeFileSurfaced(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.bin")
	// > maxScanBytes of filler; the point is it must NOT be silently ignored.
	if err := os.WriteFile(big, make([]byte, maxScanBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	out := scanSecrets(dir, []string{"big.bin"})
	if len(out) == 0 {
		t.Fatal("an unscannable over-size file must surface a finding, not be silently skipped")
	}
	if out[0].Rule != "unscanned-large-file" {
		t.Fatalf("expected an unscanned-large-file finding, got: %+v", out[0])
	}
	if out[0].Severity != SeverityBlock {
		t.Fatalf("expected the over-size finding to block, got severity %q", out[0].Severity)
	}
}

func TestScanSecrets_LongLineNotSilentlyAborted(t *testing.T) {
	dir := t.TempDir()
	// A single line > the scanner's 1<<20 buffer cap trips bufio.ErrTooLong.
	// The line also embeds an AWS-key-shaped secret earlier in the file (on a
	// short first line) to prove secrets before the long line are still
	// caught, and that the long line itself is surfaced rather than dropped.
	longLine := make([]byte, (1<<20)+1024)
	for i := range longLine {
		longLine[i] = 'a'
	}
	content := "const key = \"AKIAABCDEFGHIJKLMNOP\"\n" + string(longLine) + "\n"
	writeFile(t, dir, "big.go", content)

	out := scanSecrets(dir, []string{"big.go"})
	if len(out) == 0 {
		t.Fatal("a file with an over-long line must surface a finding, not silently drop the remainder")
	}
	foundRemainder := false
	for _, f := range out {
		if f.Rule == "unscanned-remainder" {
			foundRemainder = true
			if f.Severity != SeverityBlock {
				t.Errorf("expected unscanned-remainder to block, got severity %q", f.Severity)
			}
		}
	}
	if !foundRemainder {
		t.Fatalf("expected an unscanned-remainder finding for the over-long line, got: %+v", out)
	}
}

// TestScanText_CatchesHistoryOnlySecret verifies the history-scan path: a
// secret that a git-log-p patch ADDS in one commit (a `+`-prefixed line) is
// caught even when a later hunk REMOVES it (a `-`-prefixed line), so the net
// diff/working tree is clean. ScanText walks every added line, not the net.
func TestScanText_CatchesHistoryOnlySecret(t *testing.T) {
	// Shape mirrors `git log -p --no-color --unified=0 base..HEAD`: two commits,
	// the first ADDS the secret, the second REMOVES it (clean final tree).
	patch := "" +
		"commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"Author: x <x@example.com>\n\n    add config\n\n" +
		"diff --git a/config.env b/config.env\n" +
		"new file mode 100644\n" +
		"index 0000000..1111111\n" +
		"--- /dev/null\n" +
		"+++ b/config.env\n" +
		"@@ -0,0 +1 @@\n" +
		"+AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n" +
		"commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
		"Author: x <x@example.com>\n\n    delete config\n\n" +
		"diff --git a/config.env b/config.env\n" +
		"deleted file mode 100644\n" +
		"index 1111111..0000000\n" +
		"--- a/config.env\n" +
		"+++ /dev/null\n" +
		"@@ -1 +0,0 @@\n" +
		"-AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n"

	findings := ScanText(patch)
	if len(findings) == 0 {
		t.Fatal("expected a finding for the history-only secret, got none")
	}
	found := false
	for _, f := range findings {
		if f.Severity != SeverityBlock {
			t.Errorf("history finding must be blocking, got severity %q", f.Severity)
		}
		if f.Rule == "AWS Secret Access Key" {
			found = true
			if f.Path != "config.env" {
				t.Errorf("expected path resolved from +++ b/ header to be config.env, got %q", f.Path)
			}
			if f.Sample == "" {
				t.Error("finding must carry a (redacted) sample")
			}
			if contains(f.Sample, "wJalrXUtnFEMI") {
				t.Errorf("finding leaked the raw secret: %q", f.Sample)
			}
		}
	}
	if !found {
		t.Fatalf("expected an AWS Secret Access Key finding for the added line, got: %+v", findings)
	}
}

// TestScanText_CatchesPlusPlusContentSpoof is the F1 regression: an ADDED line
// whose CONTENT begins with "++ " renders in a git-log-p hunk body as
// "+++ <secret>" (the diff's own leading "+" prepended). The old prefix-guessing
// ScanText misclassified that as a "+++ b/…" file header and SKIPPED it, so a
// committed-then-deleted (net-zero) secret evaded the gate. A hunk-structure-aware
// scanner strips exactly one leading "+" inside a hunk body and scans "++ <secret>".
func TestScanText_CatchesPlusPlusContentSpoof(t *testing.T) {
	// Shape mirrors real `git log -p --unified=0` for a file whose sole added line
	// is `++ aws_secret_access_key=…` (verified against git): the add renders as
	// `+++ aws_secret_access_key=…` INSIDE the hunk body (after the @@).
	patch := "" +
		"commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"Author: x <x@example.com>\n\n    add\n\n" +
		"diff --git a/f b/f\n" +
		"new file mode 100644\n" +
		"index 0000000..1111111\n" +
		"--- /dev/null\n" +
		"+++ b/f\n" +
		"@@ -0,0 +1 @@\n" +
		"+++ aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n"

	found := false
	for _, f := range ScanText(patch) {
		if f.Rule == "AWS Secret Access Key" {
			found = true
			if f.Severity != SeverityBlock {
				t.Errorf("spoofed-header secret finding must block, got severity %q", f.Severity)
			}
			if contains(f.Sample, "wJalrXUtnFEMI") {
				t.Errorf("finding leaked the raw secret: %q", f.Sample)
			}
		}
	}
	if !found {
		t.Fatal("F1 bypass: `++ `-prefixed added content rendered as `+++ …` was NOT scanned — header-spoof secret-scan bypass")
	}
}

// TestScanText_CatchesDashDashAdjacencyVariant is the F1 sibling case: a removed
// line whose content starts with "-- " renders as "--- …" and an adjacent added
// line whose content starts with "++ " renders as "+++ …", mimicking the
// "--- a/…" / "+++ b/…" header PAIR. Inside a hunk body the added line is still
// content and must be scanned.
func TestScanText_CatchesDashDashAdjacencyVariant(t *testing.T) {
	patch := "" +
		"diff --git a/f b/f\n" +
		"--- a/f\n" +
		"+++ b/f\n" +
		"@@ -1 +1 @@\n" +
		"-- old placeholder line\n" +
		"+++ aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n"

	found := false
	for _, f := range ScanText(patch) {
		if f.Rule == "AWS Secret Access Key" {
			found = true
		}
	}
	if !found {
		t.Fatal("F1 variant: `++ ` add adjacent to a `-- ` remove (mimicking the ---/+++ header pair) was NOT scanned")
	}
}

// TestScanText_RealFileHeaderNotScannedAsContent verifies a genuine "+++ b/…"
// file header (in the file-header region, before the first @@) is NOT scanned as
// content even when the path itself is secret-shaped — headers are structural.
func TestScanText_RealFileHeaderNotScannedAsContent(t *testing.T) {
	// The AKIA-shaped token appears ONLY in a real +++ b/ file-header path.
	patch := "" +
		"diff --git a/AKIAABCDEFGHIJKLMNOP b/AKIAABCDEFGHIJKLMNOP\n" +
		"--- a/AKIAABCDEFGHIJKLMNOP\n" +
		"+++ b/AKIAABCDEFGHIJKLMNOP\n" +
		"@@ -1 +1 @@\n" +
		"-removed\n" +
		"+harmless\n"
	for _, f := range ScanText(patch) {
		if f.Rule == "AWS Access Key ID" {
			t.Fatalf("ScanText scanned a real +++ b/ file header as content: %+v", f)
		}
	}
}

// TestScanText_IgnoresFileHeaderAndContext verifies ScanText does not treat the
// `+++ b/…` file header as an added content line (it starts with `+` but is not
// data) and does not scan removed (`-`) or context lines.
func TestScanText_IgnoresFileHeaderAndContext(t *testing.T) {
	// The only place the secret string appears is a REMOVED line — no `+` add.
	patch := "" +
		"diff --git a/x.txt b/x.txt\n" +
		"--- a/x.txt\n" +
		"+++ b/x.txt\n" +
		"@@ -1 +1 @@\n" +
		"-const key = \"AKIAABCDEFGHIJKLMNOP\"\n" +
		"+const key = loadFromEnv()\n"
	for _, f := range ScanText(patch) {
		if f.Rule == "AWS Access Key ID" {
			t.Fatalf("ScanText flagged a removed line as an added secret: %+v", f)
		}
	}
}

// TestScanText_LongLineSurfacesUnscannedRemainder mirrors
// TestScanSecrets_LongLineNotSilentlyAborted for the history-scan path: a
// `+`-added line in a git-log-p patch longer than the scanner's 1<<20 buffer
// cap must surface an unscanned-remainder block finding rather than silently
// truncating the scan (a secret evasion route — see F1 audit follow-up).
func TestScanText_LongLineSurfacesUnscannedRemainder(t *testing.T) {
	longLine := make([]byte, (1<<20)+1024)
	for i := range longLine {
		longLine[i] = 'a'
	}
	patch := "" +
		"diff --git a/big.txt b/big.txt\n" +
		"new file mode 100644\n" +
		"--- /dev/null\n" +
		"+++ b/big.txt\n" +
		"@@ -0,0 +1 @@\n" +
		"+" + string(longLine) + "\n"

	out := ScanText(patch)
	if len(out) == 0 {
		t.Fatal("a patch with an over-long added line must surface a finding, not silently drop the remainder")
	}
	foundRemainder := false
	for _, f := range out {
		if f.Rule == "unscanned-remainder" {
			foundRemainder = true
			if f.Severity != SeverityBlock {
				t.Errorf("expected unscanned-remainder to block, got severity %q", f.Severity)
			}
		}
	}
	if !foundRemainder {
		t.Fatalf("expected an unscanned-remainder finding for the over-long added line, got: %+v", out)
	}
}

// TestScanText_FlagsBinaryAddedThenDeletedInHistory is the F1 (pass-4) case: a
// binary file ADDED in one commit and DELETED in a later commit within base..HEAD
// renders in `git log -p` only as `Binary files … differ` (no `+`-content lines),
// so its bytes are never secret-scanned, AND it's gone from the final tree so the
// working-tree scan can't see it either — a secret-carrying blob would ship in the
// pushed history undetected. ScanText must surface a `binary-in-history` block
// finding for a path both added AND deleted in the range.
func TestScanText_FlagsBinaryAddedThenDeletedInHistory(t *testing.T) {
	patch := "" +
		"commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"Author: x <x@example.com>\n\n    add blob\n\n" +
		"diff --git a/secret.bin b/secret.bin\n" +
		"new file mode 100644\n" +
		"index 0000000..abcdef1\n" +
		"Binary files /dev/null and b/secret.bin differ\n" +
		"commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
		"Author: x <x@example.com>\n\n    remove blob\n\n" +
		"diff --git a/secret.bin b/secret.bin\n" +
		"deleted file mode 100644\n" +
		"index abcdef1..0000000\n" +
		"Binary files a/secret.bin and /dev/null differ\n"

	found := false
	for _, f := range ScanText(patch) {
		if f.Rule == "binary-in-history" {
			found = true
			if f.Severity != SeverityBlock {
				t.Errorf("binary-in-history finding must block, got severity %q", f.Severity)
			}
			if f.Path != "secret.bin" {
				t.Errorf("expected path secret.bin, got %q", f.Path)
			}
		}
	}
	if !found {
		t.Fatal("F1 evasion: a binary added-then-deleted in history was NOT flagged — a secret blob could ship unscanned")
	}
}

// TestScanText_FlagsBinaryWithAndInName is the robustness regression: a binary
// whose filename literally contains " and " must still be classified add/delete
// correctly (the old SplitN(" and ") parse misclassified the delete as a MODIFY,
// letting a crafted-name secret blob evade the add+delete block).
func TestScanText_FlagsBinaryWithAndInName(t *testing.T) {
	patch := "" +
		"diff --git a/x and y.bin b/x and y.bin\n" +
		"new file mode 100644\n" +
		"Binary files /dev/null and b/x and y.bin differ\n" +
		"diff --git a/x and y.bin b/x and y.bin\n" +
		"deleted file mode 100644\n" +
		"Binary files a/x and y.bin and /dev/null differ\n"
	found := false
	for _, f := range ScanText(patch) {
		if f.Rule == "binary-in-history" && f.Severity == SeverityBlock {
			found = true
		}
	}
	if !found {
		t.Fatal("crafted binary filename containing ' and ' evaded the add+delete block")
	}
}

// TestScanText_BinaryAddedAndKeptNotFlagged is the F1 low-noise guard: a binary
// only ADDED (and kept, present in the final tree) is scannable by the
// working-tree scan and is NOT the history-evasion case, so ScanText must NOT
// emit a binary-in-history finding for it (no false positive).
func TestScanText_BinaryAddedAndKeptNotFlagged(t *testing.T) {
	patch := "" +
		"diff --git a/logo.png b/logo.png\n" +
		"new file mode 100644\n" +
		"index 0000000..abcdef1\n" +
		"Binary files /dev/null and b/logo.png differ\n"
	for _, f := range ScanText(patch) {
		if f.Rule == "binary-in-history" {
			t.Fatalf("false positive: an added-and-kept binary was flagged as history-evasion: %+v", f)
		}
	}
}

// TestScanText_BinaryDeletedOnlyNotFlagged guards the other non-evasion case: a
// pre-existing binary merely DELETED in the range (never added within it) leaves
// no unscannable secret behind — no binary-in-history finding.
func TestScanText_BinaryDeletedOnlyNotFlagged(t *testing.T) {
	patch := "" +
		"diff --git a/old.bin b/old.bin\n" +
		"deleted file mode 100644\n" +
		"index abcdef1..0000000\n" +
		"Binary files a/old.bin and /dev/null differ\n"
	for _, f := range ScanText(patch) {
		if f.Rule == "binary-in-history" {
			t.Fatalf("false positive: a delete-only pre-existing binary was flagged: %+v", f)
		}
	}
}

// TestScanText_BinaryModifiedNotFlagged guards the modify case: a binary present
// on both sides (modified, not net-removed) stays in the final tree, so the
// working-tree scan covers it — not a history-evasion, no finding.
func TestScanText_BinaryModifiedNotFlagged(t *testing.T) {
	patch := "" +
		"diff --git a/img.bin b/img.bin\n" +
		"index abcdef1..1234567 100644\n" +
		"Binary files a/img.bin and b/img.bin differ\n"
	for _, f := range ScanText(patch) {
		if f.Rule == "binary-in-history" {
			t.Fatalf("false positive: a modified (kept) binary was flagged: %+v", f)
		}
	}
}

// TestScanText_FlagsBinaryQuotedPath is the pass-5 HIGH regression: a binary
// whose filename has a non-ASCII byte (or ", \, tab, control char) is C-QUOTED by
// git in the `diff --git`/`Binary files` lines (git's default core.quotePath). The
// ADD line's b-side reads ` "b/…"` and the DELETE line's a-side reads `"a/…"`; if
// the parser fails to (a) find the quoted ` "b/` on the diff --git line, or (b)
// strip the leading `"` from the Binary-files token, the add key and delete key
// diverge → no add+delete match → a secret-carrying blob with a café-style name
// ships unflagged. Both commits emit a byte-identical `diff --git` line, so a
// correct extraction produces matching keys and a single binary-in-history block.
func TestScanText_FlagsBinaryQuotedPath(t *testing.T) {
	// git renders the non-ASCII path with octal escapes inside quotes, e.g.
	// "a/\303\251.bin" for é.bin. The bytes here are the literal backslash-octal
	// text git emits, not the decoded UTF-8.
	patch := "" +
		"commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"Author: x <x@example.com>\n\n    add blob\n\n" +
		"diff --git \"a/\\303\\251.bin\" \"b/\\303\\251.bin\"\n" +
		"new file mode 100644\n" +
		"index 0000000..abcdef1\n" +
		"Binary files /dev/null and \"b/\\303\\251.bin\" differ\n" +
		"commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
		"Author: x <x@example.com>\n\n    remove blob\n\n" +
		"diff --git \"a/\\303\\251.bin\" \"b/\\303\\251.bin\"\n" +
		"deleted file mode 100644\n" +
		"index abcdef1..0000000\n" +
		"Binary files \"a/\\303\\251.bin\" and /dev/null differ\n"

	found := false
	for _, f := range ScanText(patch) {
		if f.Rule == "binary-in-history" {
			found = true
			if f.Severity != SeverityBlock {
				t.Errorf("binary-in-history finding must block, got severity %q", f.Severity)
			}
		}
	}
	if !found {
		t.Fatal("HIGH bypass: a git-quoted (non-ASCII name) binary added-then-deleted in history was NOT flagged — a secret blob could ship unscanned")
	}
}

func TestGovulnEnv(t *testing.T) {
	t.Setenv("CORRAL_TOKEN", "SECRETVALUE")

	env := govulnEnv()

	want := []string{"GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOSUMDB=off"}
	for _, w := range want {
		found := false
		for _, e := range env {
			if e == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("govulnEnv() missing %q; got %v", w, env)
		}
	}

	for _, e := range env {
		if contains(e, "SECRETVALUE") {
			t.Fatalf("govulnEnv() leaked a secret env var into the child process: %q", e)
		}
		// GONOSUMDB is a long-removed no-op — it must not reappear.
		if contains(e, "GONOSUMDB") {
			t.Errorf("govulnEnv() sets the dead no-op GONOSUMDB: %q", e)
		}
	}
}

func contains(s, substr string) bool {
	return len(substr) > 0 && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}

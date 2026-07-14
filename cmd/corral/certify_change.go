// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"archive/tar"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// extractCommit resolves ref to a commit sha and materializes that exact
// committed tree into a fresh temp workspace via `git archive` — so the
// checks run against the commit, never the (possibly dirty) working tree.
// The returned cleanup removes the workspace; it is nil when err != nil.
func extractCommit(ref string) (workdir, sha string, cleanup func(), err error) {
	shaOut, err := exec.Command("git", "rev-parse", "--verify", "--quiet", ref+"^{commit}").Output() // #nosec G204,G702 -- fixed "git"; ref is the operator's own certify target (positional CLI arg), passed to git itself for resolution/validation, never a shell
	if err != nil {
		return "", "", nil, fmt.Errorf("resolving %q to a commit (is this a git repo?): %w", ref, err)
	}
	sha = strings.TrimSpace(string(shaOut))
	if sha == "" {
		return "", "", nil, fmt.Errorf("ref %q did not resolve to a commit", ref)
	}

	dir, err := os.MkdirTemp("", "corral-certify-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("creating workspace: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	cmd := exec.Command("git", "archive", "--format=tar", sha) // #nosec G204,G702 -- fixed "git"; sha is corral's own already-resolved commit (verified by the rev-parse above), never raw operator input
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("git archive: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("git archive: %w", err)
	}
	if err := extractTar(stdout, dir); err != nil {
		_ = cmd.Wait()
		cleanup()
		return "", "", nil, fmt.Errorf("extracting archive: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("git archive: %w", err)
	}
	return dir, sha, cleanup, nil
}

// localCertifyKeyPath mirrors cmd/corral/main.go's resolution so the CLI and
// the daemon sign with the same key file by default.
func localCertifyKeyPath() string {
	if p := strings.TrimSpace(os.Getenv("CORRALAI_CERTIFY_KEY_FILE")); p != "" {
		return p
	}
	home := ""
	if u, err := os.UserHomeDir(); err == nil {
		home = u
	} else if usr, err := user.Current(); err == nil {
		home = usr.HomeDir
	}
	return filepath.Join(home, ".claude", "corralai_certify_key")
}

func loadLocalCertifyKey() (ed25519.PrivateKey, error) {
	return buildstore.LoadOrCreateSigningKey(localCertifyKeyPath())
}

// signBuildLocally turns a raw build record into a signed, self-verifying
// buildResult using the local key — the same ledger/attestation/DSSE recipe
// as internal/brain.certifyBuild, so a locally-signed record is
// indistinguishable in shape from a brain-signed one and verifies with the
// same certverify.VerifyRecord path. Actor is the fixed local principal
// "corral-certify"; anchoring is never done here (Anchored=false).
func signBuildLocally(rec buildRecord, priv ed25519.PrivateKey) (buildResult, error) {
	const actor = "corral-certify"
	steps := []certify.Step{
		{
			Kind: "context", Actor: actor, Subject: rec.Repo + "@" + rec.Commit,
			Detail: map[string]any{"repo": rec.Repo, "commit": rec.Commit, "branch": rec.Branch},
		},
		{
			Kind: "execution", Actor: actor, Subject: rec.Command,
			Detail: map[string]any{
				"exit_code": rec.ExitCode, "ok": rec.ExitCode == 0,
				"duration_s": rec.DurationS, "output_digest": rec.OutputDigest,
			},
		},
	}
	built, head := certify.BuildLedger(steps)

	stmt := certify.BuildAttestation(certify.BuildRecord{
		Repo: rec.Repo, Commit: rec.Commit, Branch: rec.Branch, Actor: actor,
		Command: rec.Command, ExitCode: rec.ExitCode, DurationS: rec.DurationS,
		OutputDigest: rec.OutputDigest, ProducedBy: rec.ProducedBy,
	}, head)

	envelope, err := certify.SignDSSE(stmt, priv, "corral-certify")
	if err != nil {
		return buildResult{}, fmt.Errorf("signing statement: %w", err)
	}
	stepsJSON, err := certify.MarshalSteps(built)
	if err != nil {
		return buildResult{}, fmt.Errorf("marshaling steps: %w", err)
	}
	var stepsOut []map[string]any
	if err := json.Unmarshal(stepsJSON, &stepsOut); err != nil {
		return buildResult{}, fmt.Errorf("decoding steps: %w", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	return buildResult{
		ID: 0, Head: head, Signature: string(envelope), Statement: stmt,
		PublicKey: hex.EncodeToString(pub), Steps: stepsOut, Anchored: false,
	}, nil
}

// writeRecord marshals a signed buildResult into the FULL self-verifying
// record shape `corral certify verify <file>` expects (statement + signature
// + steps + head + public key + Rekor evidence) and writes it to path. Shared
// by both the legacy (--brain, --out) writer and the standalone path so the
// on-disk record shape never drifts between the two.
func writeRecord(path string, res buildResult) error {
	record := map[string]any{
		"statement":  res.Statement,
		"signature":  res.Signature,
		"steps":      res.Steps,
		"head":       res.Head,
		"public_key": res.PublicKey,
		"rekor":      res.Rekor,
		"anchored":   res.Anchored,
	}
	b, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling record: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil { // #nosec G306 -- the signed record is meant to be shared/attached to CI artifacts, not secret
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// runCertifyStandalone implements the no-brain path: check out ref into a
// clean workspace (never the possibly-dirty working tree), run the check
// jailed, sign the result locally, and write a fully self-verifying record —
// no brain round trip. Fail-closed: a jail error (no sandbox backend, setup
// failure) prints and returns 1 with NO record written, since no check was
// actually (safely) run. A normal failing check still produces a record
// ("did not pass, here's the proof") and returns the check's own exit code.
func runCertifyStandalone(ref string, checkArgv []string, outPath string, net bool, producedByFlag string, run cmdRunner, jail jailRunner, signKey func() (ed25519.PrivateKey, error), stdout, stderr io.Writer) int {
	workdir, sha, cleanup, err := extractCommit(ref)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify: %v\n", err)
		return 1
	}
	defer cleanup()

	repo := ""
	if v, err := run.GitOutput("config", "--get", "remote.origin.url"); err == nil {
		repo = v
	}
	branch := ""
	if v, err := run.GitOutput("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		branch = v
	}
	commitMessage := ""
	if v, err := run.GitOutput("show", "-s", "--format=%s", sha); err == nil {
		commitMessage = v
	}
	commitAuthor := ""
	if v, err := run.GitOutput("show", "-s", "--format=%an <%ae>", sha); err == nil {
		commitAuthor = v
	}
	commitDate := ""
	if v, err := run.GitOutput("show", "-s", "--format=%cI", sha); err == nil {
		commitDate = v
	}
	commitSignature := captureCommitSignature(run, sha)

	command := strings.Join(checkArgv, " ")
	exitCode, combined, dur, jailErr := jail.Run(context.Background(), command, workdir, net, DefaultCertifyTimeout)
	if jailErr != nil {
		// Fail closed: a jail that couldn't even run the check (no sandbox
		// backend, setup failure) is not a certifiable result — never write a
		// record claiming a check ran when it didn't, safely.
		fmt.Fprintf(stderr, "corral certify: running the check in the jail: %v\n", jailErr)
		return 1
	}
	fmt.Fprint(stdout, string(combined))

	sum := sha256.Sum256(combined)
	rec := buildRecord{
		Repo:         repo,
		Commit:       sha,
		Branch:       branch,
		Command:      command,
		ExitCode:     exitCode,
		DurationS:    dur.Seconds(),
		OutputDigest: "sha256:" + hex.EncodeToString(sum[:]),
		ProducedBy:   parseProducedBy(producedByFlag),

		CommitMessage:   commitMessage,
		CommitAuthor:    commitAuthor,
		CommitDate:      commitDate,
		CommitSignature: commitSignature,
	}

	priv, err := signKey()
	if err != nil {
		fmt.Fprintf(stderr, "corral certify: loading the local certify key: %v\n", err)
		return 1
	}
	result, err := signBuildLocally(rec, priv)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify: signing the build record: %v\n", err)
		return 1
	}

	out := outPath
	if out == "" {
		short := sha
		if len(short) > 12 {
			short = short[:12]
		}
		out = fmt.Sprintf("certify-%s.json", short)
	}
	if err := writeRecord(out, result); err != nil {
		fmt.Fprintf(stderr, "corral certify: %v\n", err)
		return exitCode
	}

	status := "pass"
	if exitCode != 0 {
		status = "fail"
	}
	fmt.Fprintf(stdout, "certified %s (%s): %s\n", sha, status, out)

	return exitCode
}

// DefaultCertifyTimeout bounds how long the standalone path's jailed check
// may run before sandbox.RunGuarded reports a timeout.
const DefaultCertifyTimeout = 600 * time.Second

// extractTar writes every regular file/dir in the tar stream under dest,
// rejecting any entry whose cleaned path would escape dest (zip-slip guard).
func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, h.Name) // #nosec G305 -- guarded below
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) && target != filepath.Clean(dest) {
			return fmt.Errorf("archive entry %q escapes the workspace", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			mode := os.FileMode(uint32(h.Mode&0o777)) & 0o777                       // #nosec G115 -- masked to 9 perm bits before conversion, cannot overflow
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) // #nosec G304 -- target is guarded against escape above
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil { // #nosec G110 -- git archive of the repo's own committed tree, not hostile input
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}

// jailRunner runs the certified check inside a sandbox. err is returned ONLY
// when the check could not be jail-run at all (no backend, setup failure) —
// the fail-closed seam; a normal nonzero check exit comes back via exitCode.
type jailRunner interface {
	Run(ctx context.Context, command, workdir string, net bool, timeout time.Duration) (exitCode int, combined []byte, dur time.Duration, err error)
}

// realJail resolves a real isolator (bwrap on linux, etc.) and runs the
// command via sandbox.RunGuarded — the one true "failed run != success"
// home. A nil/unavailable backend fails closed (never an unsandboxed run).
type realJail struct{}

func (realJail) Run(ctx context.Context, command, workdir string, net bool, timeout time.Duration) (int, []byte, time.Duration, error) {
	iso, err := sandbox.Resolve(sandbox.Config{Backend: os.Getenv("CORRALAI_EXEC_BACKEND")})
	if err != nil {
		return -1, nil, 0, fmt.Errorf("no sandbox backend (refusing to run unsandboxed): %w", err)
	}
	start := time.Now()
	res, err := sandbox.RunGuarded(ctx, command, sandbox.Options{
		Workspace: workdir, Timeout: timeout, Network: net, Backend: iso,
	})
	dur := time.Since(start)
	if err != nil {
		return -1, nil, dur, err
	}
	if res.TimedOut {
		return res.ExitCode, []byte(res.Output), dur, fmt.Errorf("check timed out after %s", timeout)
	}
	return res.ExitCode, []byte(res.Output), dur, nil
}

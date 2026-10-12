// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// fetchStdoutName is the file, in the fetch step's workspace root, that the
// jailed command's stdout is redirected to. sandbox.Run merges stdout and
// stderr into one capture; a vendoring step whose stdout is DATA (cargo
// vendor prints the source-replacement config there, its progress on
// stderr) needs them apart, and a redirect inside the jail does that without
// giving sandbox.Run a second capture mode for one caller.
const fetchStdoutName = ".corral-fetch-stdout"

// fetchMaxStdout bounds what RunFetchStep reads back from fetchStdoutName.
// The one consumer (cargo vendor's config snippet) is a few hundred bytes per
// git source; a jailed command that writes more is not producing that.
const fetchMaxStdout = 1 << 20

// RunFetchStep runs cmd inside backend, in dir — an existing staging tree
// the caller owns, not a fresh workspace — WITH the network, and returns the
// command's stdout separately from its result. It is the ONLY jailed run that
// has the network: a lang.Vendorer step that fetches a repository's
// dependencies so the offline graded runs can resolve them from the tree.
//
// Why a jail at all, when the fetch must be online: the step runs a package
// manager over a repository corral does not trust, and package managers read
// repository-controlled configuration that names programs (cargo runs a
// repo-shipped credential provider; rustup runs a repo-named toolchain). In
// the jail such a program sees no $HOME, no host environment beyond
// sandbox.FetchEnv, and writes nowhere but dir and a tmpfs.
//
// Everything that differs from a graded run lives here and nowhere else:
// Network is true and the environment is sandbox.FetchEnv. The toolchain
// binds are the graded runs' own (toolchainBinds), and the backend is the
// one the audit resolved. A nil backend refuses, exactly as a graded run
// does — there is no host fallback.
//
// stdout is read through an os.Root over dir and refused if the jailed
// command left a symlink there: the command is untrusted and must not be
// able to make corral read a host file back out.
func RunFetchStep(ctx context.Context, backend sandbox.Isolator, dir string, cmd []string, timeout time.Duration) (stdout string, res sandbox.Result, err error) {
	if backend == nil {
		return "", sandbox.Result{}, errors.New("adequacy: no sandbox backend — refusing to fetch dependencies outside the jail")
	}
	if len(cmd) == 0 {
		return "", sandbox.Result{}, errors.New("adequacy: empty command")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", sandbox.Result{}, err
	}
	defer func() { _ = root.Close() }()
	if rerr := root.Remove(fetchStdoutName); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return "", sandbox.Result{}, fmt.Errorf("adequacy: clearing %s: %w", fetchStdoutName, rerr)
	}

	binds, terr := toolchainBinds(cmd)
	if terr != nil {
		return "", sandbox.Result{}, terr
	}
	res, err = sandbox.RunGuarded(ctx, shellJoin(cmd)+" > "+shellQuote(fetchStdoutName), sandbox.Options{
		Workspace:     dir,
		Backend:       backend,
		Network:       true,
		Timeout:       timeout,
		ReadOnlyBinds: binds,
		Env:           sandbox.FetchEnv(),
		MaxOutput:     64 << 10,
		KeepTail:      true, // a failed fetch explains itself at the end
	})
	stdout, serr := readFetchStdout(root)
	if err != nil {
		return stdout, res, err
	}
	return stdout, res, serr
}

// readFetchStdout reads and removes fetchStdoutName under root, refusing
// anything but a regular file.
func readFetchStdout(root *os.Root) (string, error) {
	defer func() { _ = root.Remove(fetchStdoutName) }()
	fi, err := root.Lstat(fetchStdoutName)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("adequacy: %s is not a regular file after the fetch step; refusing to read it", fetchStdoutName)
	}
	f, err := root.Open(fetchStdoutName)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, fetchMaxStdout+1))
	if err != nil {
		return "", err
	}
	if len(b) > fetchMaxStdout {
		return "", fmt.Errorf("adequacy: the fetch step wrote more than %d bytes to stdout", fetchMaxStdout)
	}
	return string(b), nil
}

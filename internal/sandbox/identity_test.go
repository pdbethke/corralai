// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRuntime writes an executable that answers `image inspect --format
// {{.Id}} <image>` with id, or fails when id is "".
func fakeRuntime(t *testing.T, id string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "docker")
	body := "#!/bin/sh\necho 'Error: No such image' >&2\nexit 1\n"
	if id != "" {
		body = "#!/bin/sh\n[ \"$1 $2 $3 $4\" = 'image inspect --format {{.Id}}' ] || exit 3\necho " + id + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	return p
}

// A record that says "jail" cannot answer "which jail, with which
// toolchain?". The container's answer pins the image to the ID the runtime
// resolved, since a tag can be rebuilt to mean something else.
func TestIdentifyContainerPinsTheImageID(t *testing.T) {
	c := containerIsolator{runtime: fakeRuntime(t, "sha256:11855f76"), image: "corral-campaign/starlette:1"}
	id, err := Identify(c)
	if err != nil {
		t.Fatal(err)
	}
	if id.Backend != "container" || id.Image != "corral-campaign/starlette:1@sha256:11855f76" {
		t.Fatalf("Identify = %+v", id)
	}
}

// An image the runtime cannot identify is refused, never recorded as a bare
// tag: the record would claim a toolchain nobody can look up.
func TestIdentifyContainerRefusesAnUnidentifiedImage(t *testing.T) {
	c := containerIsolator{runtime: fakeRuntime(t, ""), image: "missing:1"}
	if _, err := Identify(c); err == nil || !strings.Contains(err.Error(), "missing:1") {
		t.Fatalf("Identify = %v, want an error naming the image", err)
	}
}

// Every other backend runs the host's own toolchain: the backend is the
// whole answer and there is no image.
func TestIdentifyHostBackendsHaveNoImage(t *testing.T) {
	for _, iso := range []Isolator{noneIsolator{}, namedIsolator("bwrap"), namedIsolator("sandbox-exec")} {
		id, err := Identify(iso)
		if err != nil || id.Backend != iso.Name() || id.Image != "" {
			t.Errorf("Identify(%s) = %+v, %v", iso.Name(), id, err)
		}
	}
}

type namedIsolator string

func (n namedIsolator) Name() string                                   { return string(n) }
func (namedIsolator) Preflight() error                                 { return nil }
func (namedIsolator) Wrap(string, Options, []string) ([]string, error) { return nil, nil }

// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"fmt"
	"os/exec"
	"strings"
)

// Identity is what an audit record says ran its commands: the backend and,
// for a container, the image that carried the toolchain. "jail" alone cannot
// answer the question a reader of a signed verdict will ask — which jail,
// with which toolchain — and an answer that cannot be checked is not one.
type Identity struct {
	// Backend is the isolator's Name(): "bwrap", "container", "sandbox-exec".
	Backend string
	// Image is "<ref>@<image id>" for the container backend and "" for every
	// other, which run the host's own toolchain. The ID is what the runtime
	// resolved the ref to when the scan started: a tag can be rebuilt to mean
	// something else, the ID cannot.
	Image string
}

// Identify reports iso's Identity. A container whose image the runtime cannot
// identify is an error, not a bare tag on the record.
func Identify(iso Isolator) (Identity, error) {
	c, ok := iso.(containerIsolator)
	if !ok {
		return Identity{Backend: iso.Name()}, nil
	}
	// #nosec G204 -- runtime and image are the operator's own
	// CORRALAI_EXEC_RUNTIME / CORRALAI_EXEC_IMAGE, passed as argv, no shell.
	out, err := exec.Command(c.runtime, "image", "inspect", "--format", "{{.Id}}", c.image).Output()
	id := strings.TrimSpace(string(out))
	if err != nil || id == "" {
		return Identity{}, fmt.Errorf("container backend: %s cannot identify image %q (build or pull it before the scan, so the record can name exactly what ran): %v", c.runtime, c.image, err)
	}
	return Identity{Backend: c.Name(), Image: c.image + "@" + id}, nil
}

// SPDX-License-Identifier: Elastic-2.0

package mutantsources

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Report is a mutation tool's report as the operator supplied it. One field
// now; a tool whose report is a directory of files adds its form here rather
// than widening every Reader method.
type Report struct{ Bytes []byte }

// Reader turns one mutation tool's report into mutants corral can grade. A
// tool is a Reader registered under its name, so the list of known tools has
// one source (Tools) and a new tool is one type and one Register call, not a
// new branch at every door that names tools.
type Reader interface {
	// Tool is the name --mutants-from takes, e.g. "stryker".
	Tool() string
	// Paths is the files the report covers, cleaned the way Read matches
	// them against audited paths, sorted. It refuses the reports Read refuses
	// for their shape.
	Paths(r Report) ([]string, error)
	// Label is the tool and version every record of its mutants names, e.g.
	// "stryker@10.0.0". declared is the @version the operator typed, "" when
	// none: the report's word is the record's, so a declared version that
	// contradicts the report is an error, never an override.
	Label(r Report, declared string) (string, error)
	// Read is the mutants for the audited files under each file's budget;
	// see readStryker for the contract every reader keeps.
	Read(r Report, files map[string][]byte, budget func(string) int, seed int64) (map[string]FileMutants, error)
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Reader{}
)

// Register adds r under its Tool name and returns the func that removes it
// again (for tests, which register fakes and unregister in t.Cleanup). A
// duplicate name panics naming it: two readers for one name would make which
// one grades a run depend on init order.
func Register(r Reader) (unregister func()) {
	name := r.Tool()
	// "+" joins tools in a cache key and in a seat's text, "/" "\\" and "."
	// are refused where a tool is named on the command line: a name holding
	// one would make a joined key ambiguous.
	if name == "" || strings.ContainsAny(name, "+/\\.:@") {
		panic(fmt.Sprintf("mutantsources: tool name %q is empty or holds one of + / \\ . : @", name))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("mutantsources: a reader for tool %q is already registered", name))
	}
	registry[name] = r
	return func() {
		registryMu.Lock()
		defer registryMu.Unlock()
		delete(registry, name)
	}
}

// Lookup is the reader registered for tool.
func Lookup(tool string) (Reader, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	r, ok := registry[tool]
	return r, ok
}

// Tools is the registered tool names, sorted: the only list of known tools.
func Tools() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

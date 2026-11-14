// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The console and the replay player build HTML strings with inline handlers,
// onclick="fn(<value>)", where the value is an agent name, a task key or a
// memory slug read from the brain or from a recorded tape. Those handlers
// escaped the value with .replace(/'/g,"\\'") or esc() and nothing else:
//
//   - a backslash ending the value turned the added \' into \\', an escaped
//     backslash followed by a quote that closes the JS string, so the rest of
//     the value ran as script;
//   - a double quote closed the HTML attribute itself, since the value was
//     never HTML-escaped;
//   - esc() turns ' into &#39;, which the browser decodes back to ' before the
//     handler runs, so it protects nothing inside a handler.
//
// jsArg (replay-player.js) is the one way a value enters a handler: it
// JSON-encodes the value into a JS literal and HTML-escapes the literal for
// the attribute. This test fails on any inline handler that interpolates a
// value without it.
var (
	// handlerArgs is the argument list of a call inside an inline handler
	// attribute: onclick="fn(ARGS)" or onclick="a(); fn(ARGS)".
	handlerArgs = regexp.MustCompile(`on[a-z]+=\\?"[^"]*?[A-Za-z_]\(([^)]*)\)`)
	// interp is where a value is spliced into the surrounding JS string: a
	// template ${...} or a '+ ... +' concatenation.
	interp = regexp.MustCompile(`\$\{|'\s*\+`)
	// viaJsArg is an interpolation that goes through jsArg.
	viaJsArg = regexp.MustCompile(`\$\{jsArg\(|'\s*\+\s*jsArg\(`)
	// handEscaped is the old pattern, wherever it appears.
	handEscaped = []string{`replace(/'/g`, `('${`, `(\''`}
)

func TestInlineHandlersQuoteValuesWithJsArg(t *testing.T) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "replay-player.js"} {
		raw, err := fs.ReadFile(sub, name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for _, bad := range handEscaped {
				if strings.Contains(line, bad) {
					t.Errorf("%s:%d hand-escapes a handler value (%q); use jsArg:\n%s", name, i+1, bad, strings.TrimSpace(line))
				}
			}
			for _, m := range handlerArgs.FindAllStringSubmatch(line, -1) {
				args := m[1]
				if n, ok := len(interp.FindAllString(args, -1)), len(viaJsArg.FindAllString(args, -1)); n != ok {
					t.Errorf("%s:%d an inline handler interpolates %d value(s), %d through jsArg:\n%s", name, i+1, n, ok, strings.TrimSpace(m[0]))
				}
			}
		}
	}
}

// The gate above is only as good as its patterns, so it is run against the
// shapes it must catch and the one it must pass.
func TestInlineHandlerGateCatchesTheOldShapes(t *testing.T) {
	bad := []string{
		"`<div onclick=\"selectAgent('${(a.name||'').replace(/'/g,\"\\\\'\")}')\">`",
		"`<div onclick=\"openAgentWindow('${esc(pn)}')\">`",
		"`<div onclick=\"openEntryModal('${m.slug}')\">`",
		"`<div onclick=\"openMissionDetail(${m.id})\">`",
		`'<span onclick="replayTaskClick(\'' + key + '\')">'`,
	}
	for _, line := range bad {
		caught := false
		for _, b := range handEscaped {
			caught = caught || strings.Contains(line, b)
		}
		for _, m := range handlerArgs.FindAllStringSubmatch(line, -1) {
			caught = caught || len(interp.FindAllString(m[1], -1)) != len(viaJsArg.FindAllString(m[1], -1))
		}
		if !caught {
			t.Errorf("the gate misses %s", line)
		}
	}
	good := []string{
		"`<div onclick=\"selectAgent(${jsArg(a.name)})\">`",
		"`<button onclick=\"event.stopPropagation(); openReplay(${jsArg(m.id)});\">`",
		`'<span onclick="replayTaskClick(' + jsArg(key) + ')">'`,
	}
	for _, line := range good {
		ms := handlerArgs.FindAllStringSubmatch(line, -1)
		if len(ms) == 0 {
			t.Errorf("the gate does not see the handler in %s", line)
		}
		for _, m := range ms {
			if len(interp.FindAllString(m[1], -1)) != len(viaJsArg.FindAllString(m[1], -1)) {
				t.Errorf("the gate refuses a jsArg handler: %s", line)
			}
		}
	}
}

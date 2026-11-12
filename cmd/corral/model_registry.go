// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/advpool"
)

// seatFlag ties one `--*-model` flag to the pool role it staffs, so the
// registry can resolve every seat through ONE code path instead of five
// hand-copied ones. Role is empty for a seat the pool has no role key for (the
// goal-deriver), which only means no local endpoint can be placed for it.
type seatFlag struct {
	flag string  // the flag's name, without the leading dashes
	role string  // the advpool role key, or "" when there is none
	val  *string // the parsed flag value, REWRITTEN IN PLACE on an alias hit
}

// certifySeats is the full set of seat flags a certify run parses. Kept in one
// place because "find them all" is exactly the maintenance task that goes
// wrong: a seat missed here would accept an alias, fail to resolve it, and
// send the alias itself to a provider as a model name.
func certifySeats(derive, mutant, writer, critic, shadow, shadowWriter *string) []seatFlag {
	var out []seatFlag
	if derive != nil {
		out = append(out, seatFlag{flag: "derive-model", val: derive})
	}
	if mutant != nil {
		out = append(out, seatFlag{flag: "mutant-model", role: advpool.RoleMutantGenerator, val: mutant})
	}
	if writer != nil {
		out = append(out, seatFlag{flag: "writer-model", role: advpool.RoleTestWriter, val: writer})
	}
	if critic != nil {
		out = append(out, seatFlag{flag: "critic-model", role: advpool.RoleTestCritic, val: critic})
	}
	if shadow != nil {
		out = append(out, seatFlag{flag: "shadow-model", role: advpool.RoleMutantGeneratorShadow, val: shadow})
	}
	if shadowWriter != nil {
		out = append(out, seatFlag{flag: "shadow-writer-model", role: advpool.RoleTestWriterShadow, val: shadowWriter})
	}
	return out
}

// sharedSeatProvider is the provider the test-writer and test-critic SHARE, or
// "" when they do not share one (or when either seat's provider is unknown).
//
// Provider comes from the registry where a seat named an alias, and from the
// same name-based inference the router has always used otherwise. Unknown is
// answered with "" and never guessed: claiming two local models share a vendor
// because neither name looks hosted would be a fabricated finding, and this
// line's whole value is that it is true.
//
// It NEVER gates anything. CheckDecorrelation's model-name refusal is
// unchanged; this only says out loud what that refusal cannot see.
func sharedSeatProvider(providers map[string]string, writer, critic string) string {
	writer, critic = strings.TrimSpace(writer), strings.TrimSpace(critic)
	if writer == "" || critic == "" || strings.EqualFold(critic, "off") || writer == critic {
		return ""
	}
	pw := seatProvider(providers, advpool.RoleTestWriter, writer)
	pc := seatProvider(providers, advpool.RoleTestCritic, critic)
	if pw == "" || pw != pc {
		return ""
	}
	return pw
}

// untellableAgentSeats are the graded seats sharedSeatProvider cannot judge:
// agent seats with no declared provider and no vendor corral can infer — an
// operator's own agent, unpinned (or pinned to a model no prefix names). Its
// command is theirs, so corral does not know what it runs and will not guess.
// Returned so the caller can SAY so: sharedSeatProvider answering "" for
// these reads exactly like "two different vendors", which is not known.
func untellableAgentSeats(providers map[string]string, writer, critic string) []string {
	writer, critic = strings.TrimSpace(writer), strings.TrimSpace(critic)
	if writer == "" || critic == "" || strings.EqualFold(critic, "off") {
		return nil
	}
	var out []string
	for _, s := range []struct{ role, spec string }{{advpool.RoleTestWriter, writer}, {advpool.RoleTestCritic, critic}} {
		if !isAgentSeat(s.spec) || seatProvider(providers, s.role, s.spec) != "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s (%q)", s.role, s.spec))
	}
	return out
}

// seatProvider is the provider the seat resolver recorded for role, or "".
// There is no fallback to a vendor read off the model's name: that guess
// cannot see a gateway or a local runner, and a gpt-oss name it called openai
// is how a local seat came to be routed to OpenAI. Unknown stays unknown
// (founder ruling 2026-11-12). model is unused and kept only so the callers
// read as the seat they ask about.
func seatProvider(providers map[string]string, role, _ string) string {
	return strings.TrimSpace(providers[role])
}

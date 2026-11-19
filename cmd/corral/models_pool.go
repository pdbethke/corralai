// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/autoseat"
	"github.com/pdbethke/corralai/internal/modelrank"
	"github.com/pdbethke/corralai/internal/seatrecord"
	"github.com/pdbethke/corralai/internal/shadowpool"
)

// modelsFlags is --models. --autoseat and --no-explore join it in phase 3,
// in this struct and this function, so no door can have one without the others.
//
// pool is nil until the flag is given. A plain string flag cannot tell
// `--models ""` from no flag at all, and an empty pool silently treated as
// absent would be a flag quietly ignored; nil is "not given", and anything
// given, empty included, is parsed and refused if it names nothing.
type modelsFlags struct{ pool *string }

func registerModelsFlags(fs *flag.FlagSet, help string) *modelsFlags {
	f := &modelsFlags{}
	fs.Func("models", help, func(v string) error {
		if f.pool != nil {
			// The flag package would keep the last; dropping the first pool
			// without a word is the silent substitution corral refuses.
			return errors.New("given twice: name every member in one comma-separated list")
		}
		f.pool = &v
		return nil
	})
	return f
}

// poolSeating is what a door records.
type poolSeating struct {
	SeatedBy     map[string]string // role -> operator|evidence|order; nil when --models was not given
	SeatEvidence string            // what the ranking read, as the plan's evidence line says it; "" when no seat was seated by evidence
}

// proposed reports whether --models chose any seat (by evidence or order),
// which is when a door that asks nothing says so under the plan. False
// without --models and when every seat was pinned, where no plan is printed.
func (s poolSeating) proposed() bool {
	for _, by := range s.SeatedBy {
		if by != autoseat.ByOperator {
			return true
		}
	}
	return false
}

// poolRole ties a role to the flag variable the door already has.
type poolRole struct {
	role string
	val  *string // rewritten in place with the chosen member's Given text
}

// poolDeps are seatFromPool's seams. load reads the evidence and confirm asks
// the operator; both are injected so a test touches neither the machine
// ledger nor a terminal.
type poolDeps struct {
	load rankLoader
	// confirm is handed the plan exactly as it was printed. It reports
	// whether the operator accepted and whether anyone could be asked at
	// all: with no terminal there is nobody to accept, which is not a "no".
	confirm func(plan string) (accepted bool, interactive bool, err error)
	stdout  io.Writer
	stderr  io.Writer
	// noCredentialCheck skips each member's credential check, and is set
	// by certify --repo --dry-run alone: a dry run demands no credential
	// for any seat (validateShadowPools is called there with
	// checkCredentials false), so a pool member is held to the same. The
	// zero value checks, so a door that forgets this field is strict.
	noCredentialCheck bool
}

// doorPoolDeps is the production wiring every door hands seatFromPool: the
// evidence the ranking reads from the machine ledger (defaultRankLoader, as
// `corral models rank` reads it) and a question on the terminal. A variable,
// as listOllama and shadowMemberRunnable are, because the doors' entry
// points are called from dozens of tests by a fixed signature; a test
// replaces it with a fake loader and confirm, so no door test reads the real
// ledger or a terminal. doctor and certify --repo --dry-run keep the loader
// and replace the confirm with one that asks nothing.
var doorPoolDeps = func(stdout, stderr io.Writer) poolDeps {
	return poolDeps{load: defaultRankLoader, confirm: terminalConfirm(os.Stdin, stdout), stdout: stdout, stderr: stderr}
}

// acceptUnasked is the confirm of a door that runs nothing (doctor, a dry
// run): it accepts the plan without a question, because there is nothing to
// spend and so nothing for the operator to agree to. The door then states,
// under the plan, that it asked nothing.
func acceptUnasked(string) (accepted bool, interactive bool, err error) { return true, true, nil }

// anyLangReason says why a door has no single language to rank by. It is
// spelled out per door and has no default, because the evidence line is the
// operator's record of what the ranking read, and "this scan has more than
// one" printed for a review or a doctor run without --code would be a claim
// about the scan that nobody made.
type anyLangReason string

const (
	// anyLangSeveral: a scan that audits files in more than one language.
	anyLangSeveral anyLangReason = "this scan has more than one"
	// anyLangReview: a review's seats read prose about code, and the record
	// does not rank a reviewer by the language of what it reviewed.
	anyLangReview anyLangReason = "a review is not ranked by language"
	// anyLangUnknown: doctor without --code, or with a file whose language
	// cannot be detected.
	anyLangUnknown anyLangReason = "no single language is known for this check"
)

// seatFromPool returns done=true with an exit code when the door must stop
// (refusal: 2; declined or no terminal: 0). With --models unset it returns
// a zero poolSeating and done=false, and changes nothing.
//
// The order is the spec's (5.3): the pool is parsed, then every member is
// resolved and credential-checked whether or not it will be seated, so a pool
// that works today does not fail the day a different member wins; only then
// is the evidence read, ranked and proposed. Nothing is spent before the
// operator accepts, and a lineup with an unfilled seat is never offered.
//
// lang is the scan's language, or shadowpool.LangAny when there is no single
// one (the marker certify --repo already hands the shadow draw), in which
// case anyWhy says why and is required. Neither has a default: "" is a door
// that forgot to say, and is refused.
func seatFromPool(ctx context.Context, cmdName, repoRoot, lang string, anyWhy anyLangReason, f *modelsFlags, roles []poolRole, deps poolDeps) (seating poolSeating, exit int, done bool) {
	if f == nil || f.pool == nil {
		return poolSeating{}, 0, false
	}
	refuse := func(format string, args ...any) (poolSeating, int, bool) {
		fmt.Fprintf(deps.stderr, "%s: --models: %s; not run, nothing was spent\n", cmdName, fmt.Sprintf(format, args...))
		return poolSeating{}, 2, true
	}
	if strings.TrimSpace(lang) == "" {
		return refuse("no language was given for this scan (pass the scan's language, or %q for a scan of several), so the evidence cannot be filtered", shadowpool.LangAny)
	}
	if lang == shadowpool.LangAny && strings.TrimSpace(string(anyWhy)) == "" {
		return refuse("the door gave no reason for ranking across all languages, so the evidence line cannot say why")
	}
	poolText := strings.TrimSpace(*f.pool)
	typed, err := parseModelsPool(poolText)
	if err != nil {
		return refuse("%v", err)
	}
	pool, err := resolveModelsPool(ctx, cmdName, repoRoot, typed, !deps.noCredentialCheck)
	if err != nil {
		return refuse("%v", err)
	}

	pins := map[string]string{}
	roleNames := make([]string, len(roles))
	for i, r := range roles {
		roleNames[i] = r.role
		if v := strings.TrimSpace(*r.val); v != "" {
			pins[r.role] = v
		}
	}
	if len(pins) == len(roles) {
		// Nothing to propose: every seat is the operator's, so no evidence
		// is read and nobody is asked. The pool was still checked above.
		fmt.Fprintf(deps.stderr, "%s: every seat is pinned, so --models seats nothing; the pins run as named\n", cmdName)
		by := map[string]string{}
		for _, r := range roleNames {
			by[r] = autoseat.ByOperator
		}
		return poolSeating{SeatedBy: by}, 0, false
	}

	ev, err := deps.load(rankSource{Kind: rankSourceLedger})
	if err != nil {
		return refuse("cannot read the evidence: %v — nothing was proposed", err)
	}
	report, evidence := rankPool(ev, lang, anyWhy, pool)
	plan := autoseat.Propose(autoseat.Input{
		Roles: roleNames, Pool: pool, Pins: pins, Report: report, Floor: modelrank.DefaultMinRuns,
	})

	text := renderPoolPlan(poolText, plan, evidence, ev.Generations.Line())
	fmt.Fprint(deps.stdout, text)
	if plan.Refused {
		return refuse("the proposed lineup is refused (the plan above names each unfilled seat and what would fill it)")
	}

	accepted, interactive, err := deps.confirm(text)
	switch {
	case err != nil:
		return refuse("reading the answer: %v", err)
	case !interactive:
		// Nobody to ask, so nothing runs; the operator gets the lineup as
		// flags to run deliberately. There is no --yes (spec 5.3).
		var b strings.Builder
		b.WriteString("to run this lineup:")
		for _, s := range plan.Seats {
			if s.By != autoseat.ByOperator {
				b.WriteString(" --" + autoseat.PinFlag[s.Role] + " " + s.Given)
			}
		}
		fmt.Fprintln(deps.stdout, b.String())
		return poolSeating{}, 0, true
	case !accepted:
		fmt.Fprintln(deps.stdout, "not run; nothing was spent")
		return poolSeating{}, 0, true
	}

	seating.SeatedBy = map[string]string{}
	for i, s := range plan.Seats {
		seating.SeatedBy[s.Role] = s.By
		if s.By == autoseat.ByOperator {
			continue
		}
		// The member as the operator typed it: the door's own resolution
		// runs next and rewrites an alias or vendor/model exactly as it
		// would have had the operator typed this flag.
		*roles[i].val = s.Given
		if s.By == autoseat.ByEvidence {
			seating.SeatEvidence = evidence
		}
	}
	return seating, 0, false
}

// parseModelsPool splits --models. shadowpool.ParsePool is not reused: it
// requires two members, because a challenger pool of one is no draw, whereas
// a --models pool of one is a legitimate request that seats every seat but
// the critic (the proposer refuses that seat and says so). Its other
// refusals are the same three, here: an empty member, a member named twice,
// and "off", which disables a seat and is not a model.
func parseModelsPool(text string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(text, ",") {
		m := strings.TrimSpace(raw)
		switch {
		case m == "":
			return nil, fmt.Errorf("--models %q has an empty member", text)
		case strings.EqualFold(m, "off"):
			return nil, errors.New(`--models cannot contain "off": the pool names models; to disable a seat, pin it off with its own flag (--critic-model off)`)
		case seen[strings.ToLower(m)]:
			return nil, fmt.Errorf("--models names %q twice", m)
		}
		seen[strings.ToLower(m)] = true
		out = append(out, m)
	}
	return out, nil
}

// resolveModelsPool resolves each member as its own seat, with one shared
// listing of OLLAMA_URL's daemon (shadowCandidates' pattern), and checks each
// with shadowMemberRunnable, the one per-member credential check, unless
// checkCredentials is false (poolDeps.noCredentialCheck). The
// resolver rewrites the value it is handed, so it resolves a copy and the
// member keeps what the operator typed as Given.
func resolveModelsPool(ctx context.Context, cmdName, repoRoot string, typed []string, checkCredentials bool) ([]autoseat.Member, error) {
	listing := &ollamaListing{}
	byRecorded := map[string]string{}
	var out []autoseat.Member
	for _, given := range typed {
		concrete := given
		set, err := resolveSeatsListed(ctx, cmdName, repoRoot, []seatFlag{{flag: "models", val: &concrete}}, nil, io.Discard, listing)
		if err != nil {
			return nil, fmt.Errorf("member %q: %v", given, err)
		}
		rs := set["models"]
		// Without checkCredentials (a dry run) the member is still resolved,
		// so one no source serves is refused, and is never asked for a key.
		if checkCredentials {
			if err := shadowMemberRunnable(rs); err != nil {
				return nil, fmt.Errorf("member %q cannot run: %v — every member is checked before anything is proposed, so fix or remove it", given, err)
			}
		}
		// Two spellings of one model (an alias and its concrete name) would
		// make the ranking's row ambiguous between them.
		if prev, dup := byRecorded[strings.ToLower(rs.Spec)]; dup {
			return nil, fmt.Errorf("members %q and %q are both the model %q; name it once", prev, given, rs.Spec)
		}
		byRecorded[strings.ToLower(rs.Spec)] = given
		// A bare agent seat names a tool, not a model, so the record ranks
		// nothing under it: it can be seated by order or a pin, never by
		// evidence.
		tool := ""
		if isAgentSeat(rs.Spec) {
			tool, _, _ = agentbackend.AgentSeat(rs.Spec)
		}
		out = append(out, autoseat.Member{
			Given: given, Recorded: rs.Spec, Provider: rs.Provider,
			Rankable: seatrecord.RecordedSeatNamesAModel(rs.Spec, tool),
		})
	}
	return out, nil
}

// rankPool ranks the evidence for this scan and returns the report with the
// plan's evidence line (the source verbatim, then the language it covers).
//
// A scan of several languages ranks across all of them: the language is
// cleared on a copy of the observations, so each seat is one group rather
// than one per language (the proposer reads a seat's first group only).
// Evidence that records no language cannot be filtered to one, and says so
// rather than ranking nothing.
func rankPool(ev rankEvidence, lang string, anyWhy anyLangReason, pool []autoseat.Member) (modelrank.Report, string) {
	declared := map[string]string{}
	for _, m := range pool {
		declared[m.Recorded] = m.Given
	}
	opt := modelrank.Options{MinRuns: modelrank.DefaultMinRuns, Declared: declared, Source: ev.Source}
	obs := ev.Obs
	recordsLang := false
	for _, o := range obs {
		if strings.TrimSpace(o.Lang) != "" {
			recordsLang = true
			break
		}
	}
	var scope string
	switch {
	case lang == shadowpool.LangAny:
		obs = make([]modelrank.Observation, len(ev.Obs))
		copy(obs, ev.Obs)
		for i := range obs {
			obs[i].Lang = ""
		}
		scope = "all languages (" + string(anyWhy) + ")"
	case !recordsLang:
		scope = "all languages (the evidence records none)"
	default:
		opt.Lang = lang
		scope = "language " + lang
	}
	return modelrank.Rank(obs, opt), ev.Source + "; " + scope
}

// renderPoolPlan is the plan as spec 5.3 draws it: one row per role (role,
// model, how it was chosen, why), then what the ranking read and the grading
// rules it spans. Columns are aligned by tabwriter, so a long model name
// widens its column instead of breaking the table.
func renderPoolPlan(poolText string, plan autoseat.Plan, evidence, generations string) string {
	var b strings.Builder
	b.WriteString("seats (from --models " + poolText + "):\n")
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, s := range plan.Seats {
		if s.Unfilled != "" {
			fmt.Fprintf(tw, "  %s\t—\tREFUSED\t%s\n", s.Role, s.Unfilled)
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", s.Role, seatDisplay(s), s.By, s.Reason)
	}
	_ = tw.Flush()
	if plan.Why != "" {
		b.WriteString("refused: " + plan.Why + "\n")
	}
	b.WriteString("evidence: " + evidence + "\n")
	if generations != "" {
		b.WriteString("grading rules: " + generations + "\n")
	}
	return b.String()
}

// seatDisplay is the model a seat runs, by the name the record ranks it
// under; a pinned agent seat reads "model (agent)".
func seatDisplay(s autoseat.Seat) string {
	name := s.Recorded
	if name == "" {
		name = s.Given
	}
	if agent, model, ok := agentbackend.AgentSeat(name); ok && model != "" {
		return model + " (" + agent + ")"
	}
	return name
}

// terminalConfirm asks on in, when in is a terminal, after the caller has
// printed the plan (so the plan argument is not printed again). Anything but
// y or yes declines. When in is not a terminal it asks nothing and reports
// interactive=false: a pipe or a CI log cannot accept a lineup.
func terminalConfirm(in *os.File, out io.Writer) func(plan string) (bool, bool, error) {
	return func(string) (bool, bool, error) {
		if !term.IsTerminal(int(in.Fd())) { // #nosec G115 -- a file descriptor fits in an int
			return false, false, nil
		}
		fmt.Fprint(out, "run with these seats? [y/N] ")
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, true, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, true, nil
		}
		return false, true, nil
	}
}

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
	"time"

	"golang.org/x/term"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/autoseat"
	"github.com/pdbethke/corralai/internal/hoststat"
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
	SeatedBy map[string]string // role -> operator|evidence|order; nil when --models was not given
	// SeatEvidence is what the ranking read: the source named without any
	// host path (rankEvidence.RecordSource), then the language scope exactly
	// as the plan printed it. "" when no seat was seated by evidence.
	SeatEvidence string
	// PlanWait is the interval seatFromPool spent on the plan: from reading
	// the evidence to the operator's answer. A door subtracts it from the
	// run's recorded duration, so total_millis means the same on a --models
	// run as on any other; started_at stays the run's real start. Zero when
	// no evidence was read (no --models, or every seat pinned). Not recorded:
	// it is a correction to a recorded figure, not a measurement of the run.
	PlanWait time.Duration
}

// seatEvidenceFor is the evidence line a record keeps beside seatedBy: the
// line only when a seat that record names was seated by evidence. A door
// narrows SeatedBy to the seats on its entry (a verifier that did not run,
// a roster without the deriver), and an evidence line left beside a record
// with no evidence seat would explain a choice the record does not hold.
func seatEvidenceFor(seatedBy map[string]string, evidence string) string {
	return autoseat.EvidenceFor(seatedBy, evidence)
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
	// sampler is the door's host-load sampler, paused across the plan
	// interval (PlanWait) so its figures leave out the same interval the
	// duration does. nil where the door samples nothing (doctor, review).
	sampler *hoststat.Sampler
	// placed is the door's --local-endpoint placements (role -> daemon
	// URL, as parseLocalEndpoints returns them), or nil where the door
	// takes none. A pin is resolved with them, exactly as the door's own
	// resolution will resolve it, so a pin served only by its placement
	// resolves here too.
	placed map[string]string
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
// operator's record of what the ranking read, and "the walked sources are in
// more than one" printed for a review, a doctor run without --code or a scan
// that detected no language would be a claim about the scan that nobody made.
type anyLangReason string

const (
	// anyLangSeveral: a scan whose walked sources are in more than one
	// language. It speaks of the walk, the superset the evidence pass
	// instruments, because --top or --diff-base may audit fewer files than
	// the walk found and so fewer languages.
	anyLangSeveral anyLangReason = "the walked sources are in more than one"
	// anyLangNone: a scan whose walk detected no language at all.
	anyLangNone anyLangReason = "no language was detected in this scan"
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
	listing := &ollamaListing{}
	pool, err := resolveModelsPool(ctx, cmdName, repoRoot, typed, !deps.noCredentialCheck, listing)
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

	// The pins are resolved as the members were, before any evidence is
	// read: the hard rule and the critic's provider preference compare the
	// models that would run, and a pin no source serves is refused here
	// with the resolver's own words rather than after the operator accepts.
	resolvedPins, err := resolvePins(ctx, cmdName, repoRoot, roles, pins, deps.placed, listing)
	if err != nil {
		return refuse("%v", err)
	}

	// The plan interval starts here: the evidence load, the plan and the
	// question are not the run, so the duration a door records and its host
	// sampler both leave them out. Measured once, here, so no door times it
	// separately.
	planStart := time.Now()
	deps.sampler.Pause()
	defer deps.sampler.Resume()
	ev, err := deps.load(rankSource{Kind: rankSourceLedger})
	if err != nil {
		return refuse("cannot read the evidence: %v — nothing was proposed", err)
	}
	if strings.TrimSpace(ev.RecordSource) == "" {
		// The recorded line is built from this name, never by cutting the
		// path out of the printed one; a loader that gives none has said
		// nothing the record could keep.
		return refuse("the evidence source gave no name to record without its path, so how a seat was chosen could not be recorded — nothing was proposed")
	}
	report, scope := rankPool(ev, lang, anyWhy, pool)
	plan := autoseat.Propose(autoseat.Input{
		Roles: roleNames, Pool: pool, Pins: resolvedPins, Report: report, Floor: modelrank.DefaultMinRuns,
	})

	text := renderPoolPlan(poolText, plan, ev.Source+"; "+scope, ev.Generations.Line(), len(ev.Obs) == 0)
	fmt.Fprint(deps.stdout, text)
	if plan.Refused {
		return refuse("the proposed lineup is refused (the plan above names each unfilled seat and what would fill it)")
	}

	accepted, interactive, err := deps.confirm(text)
	planWait := time.Since(planStart)
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
		fmt.Fprintln(deps.stdout, noTerminalLine)
		return poolSeating{}, 0, true
	case !accepted:
		fmt.Fprintln(deps.stdout, "not run; nothing was spent")
		return poolSeating{}, 0, true
	}

	seating.PlanWait = planWait
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
	}
	// The record keeps the source without its host path: the entry is
	// signed and may be published, and a home directory is not evidence.
	seating.SeatEvidence = seatEvidenceFor(seating.SeatedBy, ev.RecordSource+"; "+scope)
	return seating, 0, false
}

// noTerminalLine is the last line a --models run with no terminal prints. It
// exits 0, as a decline does (spec 5.3), so a pipeline that only reads the
// exit code goes green; this line is what a person reading the log sees.
const noTerminalLine = "nothing was run: --models without a terminal only prints the plan and exits 0; pass the seat flags above to run this lineup"

// resolvePins resolves every pin but "off" with the resolver the members
// use, sharing their daemon listing, and with the door's --local-endpoint
// placements. Each pin resolves a copy, so the door's flag keeps what the
// operator typed and the door's own resolution, which runs after the
// operator accepts, rewrites it exactly as it always has.
func resolvePins(ctx context.Context, cmdName, repoRoot string, roles []poolRole, pins map[string]string, placed map[string]string, listing *ollamaListing) (map[string]autoseat.Pin, error) {
	out := make(map[string]autoseat.Pin, len(pins))
	for _, r := range roles {
		given, ok := pins[r.role]
		if !ok {
			continue
		}
		if strings.EqualFold(given, "off") {
			out[r.role] = autoseat.Pin{Given: given}
			continue
		}
		flagName := autoseat.PinFlag[r.role]
		concrete := given
		set, err := resolveSeatsListed(ctx, cmdName, repoRoot, []seatFlag{{flag: flagName, role: r.role, val: &concrete}}, placed, io.Discard, listing)
		if err != nil {
			return nil, err
		}
		rs := set[flagName]
		out[r.role] = autoseat.Pin{Given: given, Recorded: rs.Spec, Provider: rs.Provider}
	}
	return out, nil
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
func resolveModelsPool(ctx context.Context, cmdName, repoRoot string, typed []string, checkCredentials bool, listing *ollamaListing) ([]autoseat.Member, error) {
	byRecorded := map[string]string{}
	var out []autoseat.Member
	for _, given := range typed {
		concrete := given
		set, err := resolveSeatsListed(ctx, cmdName, repoRoot, []seatFlag{{flag: "models", val: &concrete}}, nil, io.Discard, listing)
		if err != nil {
			var ns errNoSource
			if errors.As(err, &ns) {
				// Members resolve with no placements: a placement belongs
				// to one seat, and a member is not yet any seat's.
				return nil, fmt.Errorf("member %q: %v — a model served only through --local-endpoint cannot be a --models member; name it with its own seat flag and place it with --local-endpoint", given, err)
			}
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
// language scope the evidence line ends with. The caller puts a source in
// front of it: the printed source for the plan, the path-free one for the
// record, so both say the same scope in the same words.
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
	return modelrank.Rank(obs, opt), scope
}

// renderPoolPlan is the plan as spec 5.3 draws it: one row per role (role,
// model, how it was chosen, why), then what the ranking read and the grading
// rules it spans. Columns are aligned by tabwriter, so a long model name
// widens its column instead of breaking the table.
//
// noEvidence adds one plain line under the evidence when the ranking read no
// observation at all: the source line alone ("no entries yet") does not say
// what that means for the plan, which is that every measured seat is refused
// until it is pinned.
func renderPoolPlan(poolText string, plan autoseat.Plan, evidence, generations string, noEvidence bool) string {
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
	if noEvidence {
		b.WriteString("no recorded evidence yet: every measured seat must be pinned until this machine has runs on its ledger\n")
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

// isTerminal is term.IsTerminal, a variable so a test can say which ends of
// the conversation are terminals without allocating a pty.
var isTerminal = term.IsTerminal

// terminalConfirm asks on in, when both in and out are terminals, after the
// caller has printed the plan to out (so the plan argument is not printed
// again). Anything but y or yes declines. Otherwise it asks nothing and
// reports interactive=false: a pipe or a CI log cannot accept a lineup, and
// neither can a terminal whose plan and prompt went to a redirected stdout,
// because a "y" typed there would accept a lineup nobody saw.
func terminalConfirm(in *os.File, out io.Writer) func(plan string) (bool, bool, error) {
	return func(string) (bool, bool, error) {
		outFile, ok := out.(*os.File)
		if !ok || !isTerminal(int(in.Fd())) || !isTerminal(int(outFile.Fd())) { // #nosec G115 -- a file descriptor fits in an int
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

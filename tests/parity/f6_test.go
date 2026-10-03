package parity

import (
	"context"
	"fmt"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// The decision functions after Is and IsDetail (Which, WhichOrNone, Rate, Assess
// and their Detail forms), each against the old root's function of the same
// request, and Whether and RouteBy, which have no counterpart of the same shape in
// the old root. Their case is that the decision they ask is the request the old
// root's Whether and RouteBy ask (the old root's Is and Which), and that the
// chosen step runs once.

const (
	decideSubject  = "I was charged twice for my order."
	decideQuestion = "Which team should handle this?"
	decideRate     = "How urgent is this?"
)

// decisionPair is one call of a decision function in the old root and in version
// 2, each taking the options of its own root.
type decisionPair struct {
	old func(ctx context.Context, o types.OpOptions) error
	v2  func(ctx context.Context, o schemaflux.Options) error
}

// decisionFamily is the cases of one function: the default and each of the seven
// common settings (isSettings, which holds them for every decision), run on the
// pair, answered with reply, and the Record of the v2 call is op.
func decisionFamily(name, op, reply string, p decisionPair) []parityCase {
	var cases []parityCase
	for _, s := range isSettings() {
		cases = append(cases, decisionCase(name+"/"+s.name, op, reply, p, s))
	}
	return cases
}

// decisionCase is one pair under one setting.
func decisionCase(name, op, reply string, p decisionPair, s isSetting) parityCase {
	return parityCase{
		name: name, op: op, replies: []string{reply}, floor: schemaflux.ContractSchemaAndInvariantChecked,
		old: func(ctx context.Context) error {
			var o types.OpOptions
			s.old(&o)
			return p.old(ctx, o)
		},
		v2: func(ctx context.Context) error { return p.v2(ctx, s.v2) },
	}
}

// ownDecision is a case whose setting is the default, for the cases that vary the
// arguments and not the options.
func ownDecision(name, op, reply string, p decisionPair) parityCase {
	return decisionCase(name, op, reply, p, isSetting{name: "the default", v2: schemaflux.Options{}, old: func(*types.OpOptions) {}})
}

var decideTeams = []string{"billing", "technical", "shipping"}

func whichPair(subject any, labels []string) decisionPair {
	return decisionPair{
		old: func(ctx context.Context, o types.OpOptions) error {
			_, err := oldroot.Which(ctx, subject, decideQuestion, labels, o)
			return err
		},
		v2: func(ctx context.Context, o schemaflux.Options) error {
			_, err := schemaflux.Which(ctx, subject, decideQuestion, labels, o)
			return err
		},
	}
}

func whichDetailPair(subject any, labels []string) decisionPair {
	return decisionPair{
		old: func(ctx context.Context, o types.OpOptions) error {
			_, err := oldroot.WhichResult(ctx, subject, decideQuestion, labels, o)
			return err
		},
		v2: func(ctx context.Context, o schemaflux.Options) error {
			_, err := schemaflux.WhichDetail(ctx, subject, decideQuestion, labels, o)
			return err
		},
	}
}

func whichOrNonePair(subject any, labels []string) decisionPair {
	return decisionPair{
		old: func(ctx context.Context, o types.OpOptions) error {
			_, _, err := oldroot.WhichOrNone(ctx, subject, decideQuestion, labels, o)
			return err
		},
		v2: func(ctx context.Context, o schemaflux.Options) error {
			_, _, err := schemaflux.WhichOrNone(ctx, subject, decideQuestion, labels, o)
			return err
		},
	}
}

func whichOrNoneDetailPair(subject any, labels []string) decisionPair {
	return decisionPair{
		old: func(ctx context.Context, o types.OpOptions) error {
			_, err := oldroot.WhichOrNoneResult(ctx, subject, decideQuestion, labels, o)
			return err
		},
		v2: func(ctx context.Context, o schemaflux.Options) error {
			_, _, err := schemaflux.WhichOrNoneDetail(ctx, subject, decideQuestion, labels, o)
			return err
		},
	}
}

func ratePair(subject any, low, high int) decisionPair {
	return decisionPair{
		old: func(ctx context.Context, o types.OpOptions) error {
			_, err := oldroot.Rate(ctx, subject, decideRate, low, high, o)
			return err
		},
		v2: func(ctx context.Context, o schemaflux.Options) error {
			_, err := schemaflux.Rate(ctx, subject, decideRate, low, high, o)
			return err
		},
	}
}

func rateDetailPair(subject any, low, high int) decisionPair {
	return decisionPair{
		old: func(ctx context.Context, o types.OpOptions) error {
			_, err := oldroot.RateResult(ctx, subject, decideRate, low, high, o)
			return err
		},
		v2: func(ctx context.Context, o schemaflux.Options) error {
			_, err := schemaflux.RateDetail(ctx, subject, decideRate, low, high, o)
			return err
		},
	}
}

// parityTriage, parityEmbedding and parityLeftAlone are the structs Assess is
// asked about, the same types for both roots.
type parityTriage struct {
	Spam    bool   `ask:"Is this message spam?"`
	Team    string `ask:"Which team should handle it?" oneof:"billing,technical,shipping"`
	Urgency int    `ask:"How urgent is it?" scale:"1-5"`
}

type parityEnvelope struct {
	Language string `ask:"Which language is it written in?" oneof:"english, french"`
}

type parityEmbedding struct {
	parityEnvelope
	Spam bool `ask:"Is this message spam?"`
}

type parityLeftAlone struct {
	Spam bool   `ask:"Is this message spam?"`
	Note string `ask:"-"`
}

func assessPair[T any](subject any) decisionPair {
	return decisionPair{
		old: func(ctx context.Context, o types.OpOptions) error {
			_, err := oldroot.Assess[T](ctx, subject, o)
			return err
		},
		v2: func(ctx context.Context, o schemaflux.Options) error {
			_, err := schemaflux.Assess[T](ctx, subject, o)
			return err
		},
	}
}

func assessDetailPair[T any](subject any) decisionPair {
	return decisionPair{
		old: func(ctx context.Context, o types.OpOptions) error {
			_, err := oldroot.AssessResult[T](ctx, subject, o)
			return err
		},
		v2: func(ctx context.Context, o schemaflux.Options) error {
			_, err := schemaflux.AssessDetail[T](ctx, subject, o)
			return err
		},
	}
}

// subjectStruct is a subject that is not text.
type subjectStruct struct {
	Body string `json:"body"`
}

func whichCases() []parityCase {
	cases := decisionFamily("Which", "which", `{"q1":2}`, whichPair(decideSubject, decideTeams))
	return append(cases,
		ownDecision("Which/a struct for a subject", "which", `{"q1":1}`, whichPair(subjectStruct{Body: decideSubject}, decideTeams)),
		ownDecision("Which/labels with quotes, a line break and space", "which", `{"q1":2}`, whichPair(decideSubject, []string{`say "yes"`, "line\nbreak", " padded "})),
		ownDecision("Which/two labels", "which", `{"q1":2}`, whichPair(decideSubject, []string{"billing", "technical"})),
		ownDecision("Which/labels with a number in them", "which", `{"q1":3}`, whichPair(decideSubject, []string{"tier 1", "tier 2", "tier 3"})),
	)
}

func whichDetailCases() []parityCase {
	cases := decisionFamily("WhichDetail", "whichdetail", `{"q1":2,"confidence":0.8}`, whichDetailPair(decideSubject, decideTeams))
	return append(cases,
		ownDecision("WhichDetail/a struct for a subject", "whichdetail", `{"q1":1,"confidence":0.8}`, whichDetailPair(subjectStruct{Body: decideSubject}, decideTeams)),
	)
}

func whichOrNoneCases() []parityCase {
	cases := decisionFamily("WhichOrNone", "whichornone", `{"q1":2}`, whichOrNonePair(decideSubject, decideTeams))
	return append(cases,
		ownDecision("WhichOrNone/the answer is none", "whichornone", `{"q1":4}`, whichOrNonePair(decideSubject, decideTeams)),
		ownDecision("WhichOrNone/one label", "whichornone", `{"q1":1}`, whichOrNonePair(decideSubject, []string{"billing"})),
		ownDecision("WhichOrNone/a struct for a subject", "whichornone", `{"q1":1}`, whichOrNonePair(subjectStruct{Body: decideSubject}, decideTeams)),
	)
}

func whichOrNoneDetailCases() []parityCase {
	cases := decisionFamily("WhichOrNoneDetail", "whichornonedetail", `{"q1":2,"confidence":0.8}`, whichOrNoneDetailPair(decideSubject, decideTeams))
	return append(cases,
		ownDecision("WhichOrNoneDetail/the answer is none", "whichornonedetail", `{"q1":4,"confidence":0.6}`, whichOrNoneDetailPair(decideSubject, decideTeams)),
	)
}

func rateCases() []parityCase {
	cases := decisionFamily("Rate", "rate", `{"q1":4}`, ratePair(decideSubject, 1, 5))
	return append(cases,
		ownDecision("Rate/a scale that starts below zero", "rate", `{"q1":-1}`, ratePair(decideSubject, -2, 2)),
		ownDecision("Rate/a scale of zero to ten", "rate", `{"q1":7}`, ratePair(decideSubject, 0, 10)),
		ownDecision("Rate/the widest scale", "rate", `{"q1":57}`, ratePair(decideSubject, 1, 100)),
		ownDecision("Rate/a struct for a subject", "rate", `{"q1":4}`, ratePair(subjectStruct{Body: decideSubject}, 1, 5)),
	)
}

func rateDetailCases() []parityCase {
	cases := decisionFamily("RateDetail", "ratedetail", `{"q1":4,"confidence":0.8}`, rateDetailPair(decideSubject, 1, 5))
	return append(cases,
		ownDecision("RateDetail/a scale that starts below zero", "ratedetail", `{"q1":-1,"confidence":0.8}`, rateDetailPair(decideSubject, -2, 2)),
	)
}

func assessCases() []parityCase {
	cases := decisionFamily("Assess", "assess", `{"q1":true,"q2":2,"q3":4}`, assessPair[parityTriage](decideSubject))
	return append(cases,
		ownDecision("Assess/a struct for a subject", "assess", `{"q1":false,"q2":1,"q3":2}`, assessPair[parityTriage](subjectStruct{Body: decideSubject})),
		ownDecision("Assess/an embedded struct", "assess", `{"q1":2,"q2":true}`, assessPair[parityEmbedding](decideSubject)),
		ownDecision("Assess/a field left alone", "assess", `{"q1":true}`, assessPair[parityLeftAlone](decideSubject)),
	)
}

func assessDetailCases() []parityCase {
	cases := decisionFamily("AssessDetail", "assessdetail", `{"q1":true,"q2":2,"q3":4,"confidence":0.8}`, assessDetailPair[parityTriage](decideSubject))
	return append(cases,
		ownDecision("AssessDetail/an embedded struct", "assessdetail", `{"q1":2,"q2":true,"confidence":0.8}`, assessDetailPair[parityEmbedding](decideSubject)),
	)
}

// stepRan counts the runs of the steps of one call of Whether or RouteBy.
type stepRan map[string]int

// onlyRan fails when the step named was not run exactly once or another ran.
func (r stepRan) onlyRan(name string) error {
	for ran, n := range r {
		if (ran == name && n != 1) || (ran != name && n != 0) {
			return fmt.Errorf("step %q ran %d times: want only %q, once (%v)", ran, n, name, map[string]int(r))
		}
	}
	if r[name] != 1 {
		return fmt.Errorf("step %q ran %d times (%v), want once", name, r[name], map[string]int(r))
	}
	return nil
}

// whetherPair is one call of Whether in each root, which each asks Is and runs
// the step the answer chose, once, and no other.
func whetherPair(subject any, wantStep string) decisionPair {
	return decisionPair{
		old: func(ctx context.Context, o types.OpOptions) error {
			ran := stepRan{"then": 0, "otherwise": 0}
			step := func(name string) oldroot.Step[string] {
				return func(context.Context) (string, error) { ran[name]++; return name, nil }
			}
			if _, err := oldroot.Whether(ctx, subject, isQuestion, step("then"), step("otherwise"), o); err != nil {
				return err
			}
			return ran.onlyRan(wantStep)
		},
		v2: func(ctx context.Context, o schemaflux.Options) error {
			ran := stepRan{"then": 0, "otherwise": 0}
			step := func(name string) schemaflux.Step[string] {
				return func(context.Context) (string, error) { ran[name]++; return name, nil }
			}
			if _, err := schemaflux.Whether(ctx, subject, isQuestion, step("then"), step("otherwise"), o); err != nil {
				return err
			}
			return ran.onlyRan(wantStep)
		},
	}
}

func whetherCases() []parityCase {
	cases := decisionFamily("Whether", "is", `{"q1":true}`, whetherPair(isSubject, "then"))
	return append(cases,
		ownDecision("Whether/a no runs otherwise", "is", `{"q1":false}`, whetherPair(isSubject, "otherwise")),
		ownDecision("Whether/a struct for a subject", "is", `{"q1":true}`, whetherPair(isSubjectStruct{Body: isSubject}, "then")),
	)
}

// routeByPair is one call of RouteBy in each root, which each asks Which and runs
// the route the answer chose, once, and no other.
func routeByPair(subject any, labels []string, wantRoute string) decisionPair {
	return decisionPair{
		old: func(ctx context.Context, o types.OpOptions) error {
			ran := stepRan{}
			routes := make([]oldroot.Route[string], len(labels))
			for i, label := range labels {
				ran[label] = 0
				routes[i] = oldroot.To(label, func(context.Context) (string, error) { ran[label]++; return label, nil })
			}
			if _, err := oldroot.RouteBy(ctx, subject, decideQuestion, routes, o); err != nil {
				return err
			}
			return ran.onlyRan(wantRoute)
		},
		v2: func(ctx context.Context, o schemaflux.Options) error {
			ran := stepRan{}
			routes := make([]schemaflux.Route[string], len(labels))
			for i, label := range labels {
				ran[label] = 0
				routes[i] = schemaflux.To(label, func(context.Context) (string, error) { ran[label]++; return label, nil })
			}
			if _, err := schemaflux.RouteBy(ctx, subject, decideQuestion, routes, o); err != nil {
				return err
			}
			return ran.onlyRan(wantRoute)
		},
	}
}

func routeByCases() []parityCase {
	cases := decisionFamily("RouteBy", "which", `{"q1":2}`, routeByPair(decideSubject, decideTeams, "technical"))
	return append(cases,
		ownDecision("RouteBy/the first route", "which", `{"q1":1}`, routeByPair(decideSubject, decideTeams, "billing")),
		ownDecision("RouteBy/the last route", "which", `{"q1":3}`, routeByPair(decideSubject, decideTeams, "shipping")),
		ownDecision("RouteBy/two routes", "which", `{"q1":2}`, routeByPair(decideSubject, []string{"billing", "technical"}, "technical")),
		ownDecision("RouteBy/a struct for a subject", "which", `{"q1":1}`, routeByPair(subjectStruct{Body: decideSubject}, decideTeams, "billing")),
		ownDecision("RouteBy/labels with space around them", "which", `{"q1":2}`, routeByPair(decideSubject, []string{"billing", " technical "}, " technical ")),
	)
}

// The tests are not parallel: runParity installs an observer, which is process
// state, while it runs its last group.

func TestWhichSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, whichCases()) }

func TestWhichDetailSendsTheRequestTheOldRootsResultFormSends(t *testing.T) {
	runParity(t, whichDetailCases())
}

func TestWhichOrNoneSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, whichOrNoneCases()) }

func TestWhichOrNoneDetailSendsTheRequestTheOldRootsResultFormSends(t *testing.T) {
	runParity(t, whichOrNoneDetailCases())
}

func TestRateSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, rateCases()) }

func TestRateDetailSendsTheRequestTheOldRootsResultFormSends(t *testing.T) {
	runParity(t, rateDetailCases())
}

func TestAssessSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, assessCases()) }

func TestAssessDetailSendsTheRequestTheOldRootsResultFormSends(t *testing.T) {
	runParity(t, assessDetailCases())
}

func TestWhetherAsksTheDecisionTheOldRootsWhetherAsksAndRunsTheChosenStepOnce(t *testing.T) {
	runParity(t, whetherCases())
}

func TestRouteByAsksTheDecisionTheOldRootsRouteByAsksAndRunsTheChosenRouteOnce(t *testing.T) {
	runParity(t, routeByCases())
}

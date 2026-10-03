package main

import (
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/flowdesk/desk"
	"github.com/monstercameron/schemaflux/examples/flowdesk/sim"
	"github.com/monstercameron/schemaflux/mw"
)

// The play by play. A report of totals says how long a message took and not
// what the program did with it. With -trace each run is followed by its calls
// in the order they started: when each began, how long it took and what the
// model answered. It is the program's control flow written out, since every
// turn it took is one of those answers.

// saidLimit is how much of an answer a record keeps. The answers of a decision
// are short; a draft or a translation is clipped.
const saidLimit = 600

// short names each of the desk's questions by the field that asks it, so that
// a trace reads "team: billing" and not the question at its full length.
var short = namedQuestions()

// namedQuestions is the short name of every question the desk asks: the fields
// of the look, and the ticket match, which is asked of a message and the open
// tickets and is no field of anything.
func namedQuestions() map[string]string {
	names := fieldNames(reflect.TypeFor[desk.Look]())
	names[desk.MatchQuestion] = "ticket"
	return names
}

// fieldNames maps the `ask` tag of every field of a struct, and of the structs
// it embeds, to the field's name written as words.
func fieldNames(of reflect.Type) map[string]string {
	names := map[string]string{}
	var walk func(reflect.Type)
	walk = func(of reflect.Type) {
		for i := range of.NumField() {
			field := of.Field(i)
			if field.Anonymous && field.Type.Kind() == reflect.Struct {
				walk(field.Type)
				continue
			}
			if ask := field.Tag.Get("ask"); ask != "" {
				names[ask] = words(field.Name)
			}
		}
	}
	walk(of)
	return names
}

// words writes a field's name as words: PersonalData is "personal data".
func words(name string) string {
	var out strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) && i > 0 {
			out.WriteByte(' ')
		}
		out.WriteRune(unicode.ToLower(r))
	}
	return out.String()
}

// said is what one call's answer says, on one line. A decision is written as
// its questions and their answers. Anything else, a draft or the details read
// out of a message, is the answer's own text.
func said(operation string, req schemaflux.CompletionRequest, reply string) string {
	if operation == sim.OpAssess {
		if answers, err := sim.Answers(req, reply); err == nil {
			parts := make([]string, len(answers))
			for i, answer := range answers {
				name, known := short[answer.Question]
				if !known {
					name = clip(answer.Question, 70)
				}
				parts[i] = name + ": " + answer.Answer
			}
			return clip(strings.Join(parts, separator), saidLimit)
		}
	}
	return clip(oneLine(reply), saidLimit)
}

// oneLine puts a text on one line.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// clip cuts a text to at most limit characters, and marks the cut.
func clip(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimRightFunc(string(runes[:limit-1]), unicode.IsSpace) + "…"
}

// answerColumns is how wide a decision's answers are written before they are
// carried onto another line.
const answerColumns = 110

// callLine is how a call is written: what wait it starts, when, which
// operation, how long it took, and what it said.
const callLine = "    %s  +%6.0f ms  %-9s %6.0f ms  %s%s"

// continuation is what a line of answers after the first starts with, so that
// the answers stay under the first one: as wide as what comes before the answer
// on the line of the call.
var continuation = strings.Repeat(" ", len(fmt.Sprintf(callLine, "wait 1", 0.0, "assess", 0.0, "", "")))

// separator is what sits between the answers of a decision.
const separator = " · "

// wrapAnswers breaks the answers of a decision into lines of at most about
// width characters, at the separators between them. An answer is never broken
// in two, so a line is longer than width only for the sake of one answer that
// is longer than width alone.
func wrapAnswers(said string, width int) []string {
	var lines []string
	line := ""
	for _, answer := range strings.Split(said, separator) {
		if line != "" && utf8.RuneCountInString(line)+utf8.RuneCountInString(separator)+utf8.RuneCountInString(answer) > width {
			lines = append(lines, line+" ·")
			line = ""
		}
		if line != "" {
			line += separator
		}
		line += answer
	}
	return append(lines, line)
}

// playByPlay writes one run as it happened.
func playByPlay(stdout io.Writer, labelled sim.Case, outcome desk.Outcome, made record) {
	fmt.Fprintf(stdout, "\n    %s, %s: %s\n", labelled.Message.ID, labelled.Kind,
		clip(oneLine(labelled.Message.Subject+" | "+labelled.Message.Body), 130))

	calls := append([]call(nil), made.Operations...)
	sort.SliceStable(calls, func(i, j int) bool { return calls[i].StartMS < calls[j].StartMS })

	// A call that starts once every earlier one has ended starts a new wait.
	// The times are whole microseconds written as milliseconds, and a clock
	// as coarse as Windows' gives a call the very microsecond the one before
	// it ended in, so the comparison allows for the sum's rounding.
	const rounding = 0.0005
	wait, busyUntil := 0, -1.0
	for _, one := range calls {
		mark := "      "
		if one.StartMS >= busyUntil-rounding {
			wait++
			mark = fmt.Sprintf("wait %d", wait)
		}
		busyUntil = max(busyUntil, one.StartMS+one.MS)
		failed := ""
		if one.Failed {
			failed = "FAILED "
		}
		// What a decision answered is written in full, wrapped under its first
		// line: the answers that decide the path are the last of its questions
		// as often as the first. A draft, a translation and the rest are clipped.
		lines := []string{clip(one.Said, 170)}
		if one.Operation == sim.OpAssess {
			lines = wrapAnswers(one.Said, answerColumns)
		}
		fmt.Fprintf(stdout, callLine+"\n", mark, one.StartMS, one.Operation, one.MS, failed, lines[0])
		for _, more := range lines[1:] {
			fmt.Fprintf(stdout, "%s%s%s\n", continuation, strings.Repeat(" ", len(failed)), more)
		}
	}

	ticket := "ticket " + outcome.TicketID
	if outcome.New {
		ticket += " (opened)"
	}
	if outcome.TicketID == "" {
		ticket = "no ticket"
	}
	redrafted := ""
	if outcome.Redrafted {
		redrafted = ", redrafted after the audit"
	}
	fmt.Fprintf(stdout, "    done    +%6.0f ms  %s, %s%s\n", made.WallMS, made.Status, ticket, redrafted)
	if outcome.Reply != "" {
		fmt.Fprintf(stdout, "            reply: %s\n", clip(oneLine(outcome.Reply), 170))
	}
	fmt.Fprintln(stdout)
}

// hedging counts what a hedge did over a run.
type hedging struct {
	calls, sentTwice, secondWon atomic.Int64
}

func (h *hedging) saw(event mw.HedgeEvent) {
	h.calls.Add(1)
	if event.Hedged {
		h.sentTwice.Add(1)
	}
	if event.SecondWon {
		h.secondWon.Add(1)
	}
}

// through returns the provider with a hedge round it, or the provider as it
// is when no delay was asked for.
func (h *hedging) through(provider schemaflux.Provider, after time.Duration) schemaflux.Provider {
	if after <= 0 {
		return provider
	}
	return mw.Chain(provider, mw.Hedge(after, mw.WithHedgeObserver(h.saw)))
}

// report says how often the second copy was sent and how often it won. The
// second copies are requests the provider bills for and the client's ledger
// does not see, so the cost columns above are a floor when any were sent.
func (h *hedging) report(stdout io.Writer, after time.Duration) {
	if after <= 0 {
		return
	}
	fmt.Fprintf(stdout, "\nhedge after %v: %d of %d calls were sent a second time, and the second copy answered first in %d. "+
		"The second copies are not in the cost above.\n",
		after, h.sentTwice.Load(), h.calls.Load(), h.secondWon.Load())
}

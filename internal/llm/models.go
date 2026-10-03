package llm

import (
	"net/url"
	"strings"
)

// What the request builder knows about a model lives in this one table.
//
// It used to live in three functions that each compared a prefix: one for
// temperature, one for whether to send a reasoning block, one for the effort
// value. They agreed only by accident, and all three answered for a model none
// of them named. On 2026-10-01 that default was wrong for a whole new family:
// gpt-6-luna rejected `temperature` and every operation in the library returned
// HTTP 400 (TODOS.md LX-001). These parameters are not negotiated, so one the
// API does not accept fails the whole request, and what an unknown model gets
// has to be decided here on purpose.

// modelTraits is one row: how to shape a request for a family.
type modelTraits struct {
	// temperature reports whether the model accepts the `temperature`
	// parameter. False means the key is left off the request.
	temperature bool

	// reasoningBlock reports whether a `reasoning` block is sent when the
	// caller has not asked for a particular effort.
	reasoningBlock bool

	// defaultEffort is the effort sent when reasoningBlock is true, or a value
	// the family is known to accept when it is false. "" means none is known.
	defaultEffort string

	// efforts lists the reasoning efforts the family accepts, lowest first.
	// Nil means they have not been observed, which is different from an empty
	// list: an empty list means the family is known to take none, because it
	// does not reason. resolveEffort reads the difference.
	efforts []string
}

// measuredPreferences lists, by model, the efforts an operation may prefer on
// it: the ones measured on that model and found to cost a judgment nothing. A
// caller may ask for any effort a family accepts. An operation asking on the
// caller's behalf may ask only for what has been shown to be safe, and "the
// API accepts it" is not that: the 5.6 family accepts none, and none took two
// of its models from four right in four to none.
//
// The names are models, not families. This was a row in the table above, which
// is matched by prefix, so "gpt-6-luna-mini" and a fine-tune of gpt-6-luna
// inherited a measurement nobody made on them. A name here stands for itself
// and for its dated snapshots, and for nothing else.
//
// gpt-6-luna was measured on 2026-10-02 (TODOS.md LX-105). gpt-6-sol,
// gpt-6-astra and gpt-6.1 are not here until someone measures them.
var measuredPreferences = map[string][]string{
	"gpt-6-luna": {"none"},
}

// preferenceMeasured reports whether an operation's preference for effort was
// measured on this model.
func preferenceMeasured(model, effort string) bool {
	// A fine-tune is a different model from the one it was tuned from.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "ft:") {
		return false
	}
	// A gateway's spelling of the same model is the same model.
	name := canonicalModelName(model)
	for _, measured := range measuredPreferences[withoutSnapshotDate(name)] {
		if measured == effort {
			return true
		}
	}
	return false
}

// withoutSnapshotDate strips a trailing "-YYYY-MM-DD" from a model name, which
// is how a dated snapshot of a model is named.
func withoutSnapshotDate(name string) string {
	const shape = "-0000-00-00"
	if len(name) <= len(shape) {
		return name
	}
	suffix := name[len(name)-len(shape):]
	for i := 0; i < len(shape); i++ {
		digit := suffix[i] >= '0' && suffix[i] <= '9'
		if (shape[i] == '0') != digit || (shape[i] == '-' && suffix[i] != '-') {
			return name
		}
	}
	return name[:len(name)-len(shape)]
}

// WithoutVersionTag strips the tag that names one version of a model from the
// end of its name: a date, written "-YYYY-MM-DD" or as eight digits, or a
// number of three digits or more, after "-", "@" or ":". Providers answer
// under such names: a request for "gpt-4o" is answered by "gpt-4o-2024-08-06",
// one for a Claude model by its name with "-20250929" on the end, one for a
// Gemini model by its name with "-001".
//
// A name with nothing before the tag, and a final part that is not all digits
// ("gpt-4-32k", "model:latest", "gpt-6-luna-mini") or is shorter than three
// ("claude-3-5"), is returned as it is: those are other models.
func WithoutVersionTag(name string) string {
	if dated := withoutSnapshotDate(name); dated != name {
		return dated
	}
	at := strings.LastIndexAny(name, "-@:")
	if at <= 0 {
		return name
	}
	tag := name[at+1:]
	if len(tag) < 3 {
		return name
	}
	for i := 0; i < len(tag); i++ {
		if tag[i] < '0' || tag[i] > '9' {
			return name
		}
	}
	return name[:at]
}

// ModelSender is implemented by a provider that sends a different model from
// the one a request names. The route a request takes is the provider and the
// model it sends, and a check made on the name in the request is a check on a
// route that is not the one used.
type ModelSender interface {
	// ModelSentFor returns the model the provider sends when asked for requested.
	ModelSentFor(requested string) string
}

// ModelSentBy returns the model provider will send for a request that names
// requested: what ModelSender says, or requested itself for a provider that
// does not implement it. A provider wrapped by a middleware that does not
// forward ModelSender reports the name as requested; that is a recorded limit.
func ModelSentBy(provider Provider, requested string) string {
	if sender, ok := provider.(ModelSender); ok {
		return sender.ModelSentFor(requested)
	}
	return requested
}

// SameModelRoute reports whether the model an answer names is the model that
// was asked for. Neither can be empty. Names are compared without regard to
// capitals, and these are the same route:
//
//   - the same name;
//   - the asked name with a version tag on the end ("gpt-4o" answered by
//     "gpt-4o-2024-08-06");
//   - an asked snapshot answered by its base alias ("gpt-4o-2024-08-06"
//     answered by "gpt-4o"), since a provider names the alias when the snapshot
//     it resolved to is not the one that was pinned;
//   - an asked "-latest" alias answered by a snapshot or by the bare name
//     ("claude-3-5-sonnet-latest" answered by "claude-3-5-sonnet-20241022",
//     "gemini-1.5-pro-latest" by "gemini-1.5-pro-002");
//   - a gateway-prefixed request answered without the prefix ("openai/gpt-4o"
//     answered by "gpt-4o-2024-08-06"), which is how a gateway reports the model
//     a vendor served.
//
// A dotted version ("-1.13.0") is read as a tag in one case: the model asked for
// is a base name, asked as its "-latest" alias or bare, and the answer is that
// base name and the version ("jev-latest" answered by "jev-1.13.0"). The API
// resolves the alias and says which version it ran. A dotted number is not a tag
// anywhere else, because there it names a model: "gpt-6" is not "gpt-6.1.0" and
// "gpt-5.4" is not "gpt-5.4.1".
//
// Only the version tags WithoutVersionTag recognises, and that one, are ignored,
// so a model the tag rules leave alone is another model: "gpt-4" is not
// "gpt-4-32k" or "gpt-4-1106-preview", "gpt-4o" is not "gpt-4o-mini-2024-07-18",
// and "jev-latest" is not "jevx-1.13.0" or "jev-mini-1.13.0".
//
// Recorded limits, both refused: a rename that is not a version tag, such as
// "gpt-4-turbo-preview" answered by "gpt-4-0125-preview", and an Azure
// deployment name answered by the model it deploys. Nothing in the names says
// those are the same model, and a data policy that accepted any pair that might
// be would accept a forbidden model; an operator who uses them declares the
// answered name as a route.
func SameModelRoute(asked, answered string) bool {
	asked, answered = strings.TrimSpace(asked), strings.TrimSpace(answered)
	if asked == "" || answered == "" {
		return false
	}
	answeredBase := WithoutVersionTag(answered)
	answeredUnversioned, dotted := withoutDottedVersion(answered)
	for _, form := range askedForms(asked) {
		if strings.EqualFold(form, answered) || strings.EqualFold(form, answeredBase) {
			return true
		}
		// An asked snapshot, answered by the alias it is a snapshot of.
		if base := WithoutVersionTag(form); base != form && strings.EqualFold(base, answered) {
			return true
		}
		// An asked "-latest" alias, answered by the name it stands for or by a
		// snapshot of it.
		alias, isAlias := withoutLatestSuffix(form)
		if isAlias && (strings.EqualFold(alias, answered) || strings.EqualFold(alias, answeredBase)) {
			return true
		}
		// A base name, asked as its alias or bare, answered by a dotted version of
		// it.
		if dotted {
			base := form
			if isAlias {
				base = alias
			}
			if strings.EqualFold(base, answeredUnversioned) {
				return true
			}
		}
	}
	return false
}

// withoutDottedVersion strips a version of two or more numbers joined by dots
// ("-1.13.0", "-1.13") from the end of a name, after the last "-", and reports
// whether there was one. A name with nothing before the hyphen, a part that is
// empty or not all digits ("-1.", "-1.x", "-v1.13.0") and a final part with no
// dot in it are not versions and are returned as they are.
func withoutDottedVersion(name string) (string, bool) {
	at := strings.LastIndex(name, "-")
	if at <= 0 {
		return name, false
	}
	parts := strings.Split(name[at+1:], ".")
	if len(parts) < 2 {
		return name, false
	}
	for _, part := range parts {
		if part == "" {
			return name, false
		}
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				return name, false
			}
		}
	}
	return name[:at], true
}

// askedForms lists the spellings an asked model may be answered under: the
// name as asked, without the "~" of a gateway's alias, and without a gateway's
// vendor prefix when it has one.
func askedForms(asked string) []string {
	forms := []string{asked}
	// OpenRouter marks an alias that follows a family's newest version with a
	// leading "~": "~typesafe/jev-latest" is the model "typesafe/jev-latest".
	if strings.HasPrefix(asked, "~") && len(asked) > 1 {
		forms = append(forms, asked[1:])
	}
	if slash := strings.LastIndex(asked, "/"); slash >= 0 && slash < len(asked)-1 {
		forms = append(forms, asked[slash+1:])
	}
	return forms
}

// withoutLatestSuffix strips a trailing "-latest" from a model name. It reports
// false when the name does not end in it or nothing would be left.
func withoutLatestSuffix(name string) (string, bool) {
	const suffix = "-latest"
	if len(name) <= len(suffix) || !strings.EqualFold(name[len(name)-len(suffix):], suffix) {
		return name, false
	}
	return name[:len(name)-len(suffix)], true
}

// The efforts the API named when it rejected one, 2026-10-01, for gpt-6-luna:
// "Supported values are: 'none', 'low', 'medium', 'high', 'xhigh', and 'max'."
// The gpt-5.6 family reported the same list when it was measured.
var effortsNoneToMax = []string{"none", "low", "medium", "high", "xhigh", "max"}

// modelFamilies maps a name prefix to its traits. The longest matching prefix
// wins, so the order of the rows does not matter and a row added later cannot
// be shadowed by a shorter one above it.
var modelFamilies = map[string]modelTraits{
	// Seen live 2026-10-01 on gpt-6-luna: "Unsupported parameter: 'temperature'
	// is not supported with this model." No reasoning block is sent by
	// default, so the API's own default effort (medium) applies.
	//
	// Measured on gpt-6-luna, 2026-10-02 (TODOS.md LX-105). On 18 labelled
	// judgment tasks, 355 calls an effort:
	//
	//	          score    median     p90      reasoning tokens
	//	none      0.988    1467ms    2721ms      0
	//	low       0.979    1991ms    4084ms     72
	//	medium    0.985    2077ms    5146ms    105
	//
	// and on four arithmetic tasks, six runs each: none 13 of 24, low 24 of 24,
	// medium 24 of 24. With reasoning off the model judges as well as it ever
	// does and cannot be trusted to compute. That is why there is no default
	// effort in this row: the right one depends on what is being asked, which
	// an operation knows and this table does not.
	//
	// The measurement was made on gpt-6-luna and no other model, which is why
	// an operation's preference for no reasoning is honoured there alone: see
	// measuredPreferences.
	"gpt-6": {efforts: effortsNoneToMax},

	// Measured: all three 5.6 models reject `temperature` outright, including
	// a temperature of zero.
	//
	// The family accepts a reasoning block, so leaving it off is not about what
	// the API allows. Measured on a proration with a distractor figure, four
	// runs per arm:
	//
	//	                 omitted        effort=none      effort=low
	//	luna             4/4  2304ms    0/4  3682ms      4/4  2339ms
	//	sol              4/4  6808ms    0/4  2959ms      4/4  3375ms
	//	terra            4/4  2174ms    4/4  1884ms      4/4  2232ms
	//
	// `effort: none` takes luna and sol from four correct to none correct --
	// answers of 650 and 2600 against a correct 500. `effort: low` matches the
	// omitted case on accuracy and beats it on latency nowhere that matters. So
	// the block is omitted and the server's default stands. "low" is still
	// named, because the family rejects "minimal", and a row that is one flag
	// away from a valid request is safer than one that is one flag away from a
	// 400.
	"gpt-5.6": {defaultEffort: "low", efforts: effortsNoneToMax},

	"gpt-5.4": {reasoningBlock: true, defaultEffort: "none"},

	// Carried over unchanged from the three functions this table replaced.
	// "minimal" was measured on gpt-5, gpt-5-mini and gpt-5-nano only; whether
	// every later gpt-5.x accepts it has not been checked (LX-005).
	"gpt-5": {reasoningBlock: true, defaultEffort: "minimal"},

	// These do not reason, and reject a `reasoning` block outright. The empty
	// list says so; nil would say "not observed".
	"gpt-4":   {temperature: true, efforts: []string{}},
	"gpt-3.5": {temperature: true, efforts: []string{}},

	// The o-series are reasoning models and reject `temperature`.
	"o1": {},
	"o3": {},
	"o4": {},
}

// traitsFor returns the traits for a model on a given endpoint.
//
// A name the table knows gets its row wherever it is served: gpt-6-luna
// rejects `temperature` through a gateway as surely as it does directly.
//
// A name it does not know is decided by the endpoint, because the name alone
// cannot say who is serving it. On OpenAI's own endpoint an unknown name is a
// model newer than this table, and every family OpenAI has shipped since gpt-5
// rejects `temperature`; so it is left off, which costs a caller the Mode
// setting on a model that would have taken it, where sending it costs every
// request on a model that does not. On any other endpoint an unknown name is
// usually a local Gemma or Llama behind an OpenAI-shaped server. Those accept
// `temperature`, it is what this library has always sent them, and that is what
// they still get.
//
// The case this cannot get right is an alias: an Azure deployment or a proxy
// route that names a gpt-6 model something else. That is an unknown name on a
// host that is not OpenAI's, so it is sent `temperature` and refused.
// ProviderConfig.OmitTemperature is the switch for it.
func traitsFor(model string, openAIEndpoint bool) modelTraits {
	name := canonicalModelName(model)

	best := ""
	for prefix := range modelFamilies {
		if strings.HasPrefix(name, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best != "" {
		return modelFamilies[best]
	}
	return modelTraits{temperature: !openAIEndpoint}
}

// canonicalModelName strips the decoration a name can carry around the family
// it belongs to, so a fine-tune or a gateway's spelling still finds its row:
// "ft:gpt-4o:org::id" and "openai/gpt-6-luna" are a gpt-4 and a gpt-6 model.
func canonicalModelName(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	name = strings.TrimPrefix(name, "ft:")
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	return name
}

// isOpenAIEndpoint reports whether a configured base URL is OpenAI's own API:
// api.openai.com, or one of its regional hosts such as eu.api.openai.com. An
// empty base URL is the default, which is.
func isOpenAIEndpoint(baseURL string) bool {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return true
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "api.openai.com" || strings.HasSuffix(host, ".api.openai.com")
}

// acceptsEffort reports whether a family is known to accept a reasoning effort.
// It is false for a family whose accepted values have not been observed.
func (traits modelTraits) acceptsEffort(effort string) bool {
	for _, accepted := range traits.efforts {
		if accepted == effort {
			return true
		}
	}
	return false
}

// resolveEffort decides the reasoning effort a request carries, or "" for no
// `reasoning` block. problem is non-empty when the request has to be refused.
//
// requested is the effort asked for, and preferred says who asked. A caller's
// own value (preferred false) is a requirement: it is sent, or the request is
// refused with the values the family takes. These parameters are not
// negotiated, so a value the API does not accept fails the whole request, and
// refusing here says why before a round trip does. An operation's preference
// (preferred true) is a suggestion: it is sent only where it was measured to be
// safe for a judgment (see measuredPreferences), and otherwise the request
// is what it would have been without it.
//
// With nothing requested the answer is what was sent before the option
// existed: the family's default when its row says to send one, and nothing
// otherwise.
//
// A caller's value for a family whose efforts have not been observed is sent
// as given. Refusing it would make the option unusable on every model newer
// than this table, and the API says so itself if the value is wrong.
func resolveEffort(model string, openAIEndpoint bool, requested string, preferred bool) (effort, problem string) {
	traits := traitsFor(model, openAIEndpoint)

	unset := ""
	if traits.reasoningBlock {
		unset = traits.defaultEffort
	}
	if requested == "" {
		return unset, ""
	}

	if preferred {
		if preferenceMeasured(model, requested) && traits.acceptsEffort(requested) {
			return requested, ""
		}
		return unset, ""
	}
	if traits.acceptsEffort(requested) {
		return requested, ""
	}
	if traits.efforts == nil {
		return requested, ""
	}
	if len(traits.efforts) == 0 {
		return "", "reasoning effort \"" + requested + "\" was asked for, and this model does not take a reasoning effort"
	}
	return "", "reasoning effort \"" + requested + "\" is not one this model takes; it takes " + strings.Join(traits.efforts, ", ")
}

package ops

import (
	"context"
	"unicode"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/internal/types"
)

// An operation can prefer a reasoning effort for its own calls.
//
// A preference is not the caller's option. The caller's, types.OpOptions.Effort,
// is a requirement: a model that does not take the value refuses the request.
// A preference is dropped where it cannot apply, so an operation can ask for
// less reasoning without making itself unusable on a model that has no such
// setting. It travels on the context because it belongs to the operation's
// call and not to the options a caller sees.
//
// Which operations prefer what is a measured decision, not a guess. On
// gpt-6-luna, with reasoning off, judgments scored as they do at the default
// and were a third faster, and arithmetic failed nearly half the time. So the
// operations that prefer none are the ones whose answer is a semantic
// judgment: Classify, Similar and Deduplicate. An operation built around the
// caller's own criteria or conditions (Choose, Decide, Match, Sort, Rank,
// Filter, Question, Score) prefers nothing, because "the cheapest per unit" and
// "the total exceeds $500" are arithmetic.
//
// The three still take text from the caller: categories with descriptions and
// examples, aspects, steering, and the items themselves. A description that
// reads "line items total more than $500" turns a classification into a sum.
// So the preference is made per call, by judgmentEffort: it is none only when
// nothing the caller supplied mentions a number.

type effortPreferenceKey struct{}

// preferEffort returns a context whose calls prefer the given effort.
func preferEffort(ctx context.Context, effort types.Effort) context.Context {
	if effort == types.EffortDefault {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, effortPreferenceKey{}, effort)
}

// preferredEffort reads the operation's preference, or EffortDefault.
func preferredEffort(ctx context.Context) types.Effort {
	if ctx == nil {
		return types.EffortDefault
	}
	effort, _ := ctx.Value(effortPreferenceKey{}).(types.Effort)
	return effort
}

// requestEffort is what a request carries: the effort the call names, which is
// a requirement; and otherwise the client's, and then the operation's, which
// are preferences and are marked as such.
func requestEffort(ctx context.Context, opts types.OpOptions) (effort string, preferred bool) {
	if opts.Effort != types.EffortDefault {
		return string(opts.Effort), false
	}
	// The client's is a preference, made once for every call it makes: it is
	// sent on a route that takes an effort and dropped on one that does not. It
	// was sent as a requirement, so on a provider that accepts none, Anthropic
	// and every chat-completions provider, every call the client made failed,
	// and so did the alternate a fallback chain had for such a call. An effort a
	// call names itself is still refused where it cannot be sent, since that
	// call asked for it.
	if named := ExecEffort(ctx); named != types.EffortDefault {
		return string(named), true
	}
	if preference := preferredEffort(ctx); preference != types.EffortDefault {
		return string(preference), true
	}
	return "", false
}

// judgmentEffort is the effort a judgment prefers, given every piece of text
// the caller supplied for it: none when none of them mentions a number, and no
// preference when one does.
//
// What reasoning buys on this kind of call is arithmetic, and arithmetic needs
// numbers. A number is not proof that anything will be computed, and when one
// is there the call runs as it would have without this: slower and no less
// right. The two mistakes are not equal, a needless default costs half a
// second and a missed number can cost the answer, so the test is wide. It
// counts "2FA", "the two of us" and "the third quarter".
//
// What it cannot see is a number written as a word in a language other than
// English ("zwölf Einheiten"). That is a stated limit: a caller with such
// input sets the effort.
func judgmentEffort(texts ...string) types.Effort {
	for _, text := range texts {
		if mentionsANumber(text) {
			return types.EffortDefault
		}
	}
	return types.EffortNone
}

// mentionsANumber reports whether text has a numeral in it or an English word
// for a quantity.
//
// A numeral is any character Unicode classes as a number, which takes in the
// decimal digits of every script, fractions such as "½", superscripts and
// roman numeral signs, and the Han numerals, which Unicode classes as letters.
// A quantity word counts only as a whole word: "ten" does, "often" and
// "tenant" do not.
//
// It reads the whole of every call's input, so it does not allocate and takes
// the ASCII bytes without decoding them.
func mentionsANumber(text string) bool {
	var word [longestQuantityWord]byte
	// length is how many letters the word being read has. A word longer than
	// the buffer cannot be on the list, and neither can one with a letter in
	// it from outside ASCII, which is given a length past the buffer.
	length := 0
	for i := 0; i < len(text); {
		c := text[i]
		if c < utf8.RuneSelf {
			i++
			switch {
			case c >= '0' && c <= '9':
				return true
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
				if length < len(word) {
					word[length] = c | 0x20
				}
				length++
			default:
				if isQuantityWord(word[:], length) {
					return true
				}
				length = 0
			}
			continue
		}

		r, size := utf8.DecodeRuneInString(text[i:])
		i += size
		// Kana and the unified Han block hold no character Unicode classes as
		// a number, and are written without spaces, so each is a word of its
		// own: only the Han numerals need looking for. This is most of what a
		// Japanese or Chinese input is made of, and it skips three table
		// lookups a character.
		if (r >= 0x3040 && r <= 0x30FF) || (r >= 0x4E00 && r <= 0x9FFF) {
			if isHanNumeral(r) || isQuantityWord(word[:], length) {
				return true
			}
			length = 0
			continue
		}
		if unicode.IsNumber(r) || isHanNumeral(r) {
			return true
		}
		// A letter joins the word it is in, "tenía" is not "ten", except in
		// the scripts written without spaces, where a letter is a word.
		if unicode.IsLetter(r) && !unicode.In(r, unspacedScripts...) {
			length = len(word) + 1
			continue
		}
		if isQuantityWord(word[:], length) {
			return true
		}
		length = 0
	}
	return isQuantityWord(word[:], length)
}

// longestQuantityWord is the length of "percentage".
const longestQuantityWord = 10

// isQuantityWord reports whether the letters read are an English word that
// puts a number in play. "One" is left out: it is an article or a pronoun far
// more often than a quantity.
//
// It is asked at the end of every word of every input, so it is a switch, which
// the compiler turns into a comparison by length and then by value, and not a
// map, which would hash each word.
func isQuantityWord(word []byte, length int) bool {
	if length < 3 || length > len(word) {
		return false
	}
	switch string(word[:length]) {
	case "zero", "two", "three", "four", "five", "six", "seven", "eight", "nine",
		"ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen",
		"seventeen", "eighteen", "nineteen", "twenty", "thirty", "forty", "fifty",
		"sixty", "seventy", "eighty", "ninety",
		"hundred", "hundreds", "thousand", "thousands", "million", "millions",
		"billion", "billions", "trillion", "trillions", "dozen", "dozens",
		// Fractions and multiples. "Third", "quarter" and "fifth" are also an
		// ordinal, a season of the year's accounts and a measure of spirits;
		// counting them costs a call its speed-up and nothing else.
		"half", "halves", "third", "thirds", "quarter", "quarters", "fourth", "fourths",
		"fifth", "fifths", "sixth", "sixths", "seventh", "sevenths", "eighth", "eighths",
		"ninth", "ninths", "tenth", "tenths", "twice", "thrice", "double", "triple", "quadruple",
		"percent", "percentage":
		return true
	}
	return false
}

// isHanNumeral reports whether r is one of the Han characters that write a
// number. Unicode classes them as letters, so IsNumber does not see them. The
// first of them is also an everyday word, and is counted all the same.
func isHanNumeral(r rune) bool {
	switch r {
	case '一', '二', '三', '四', '五', '六', '七', '八', '九', '十',
		'百', '千', '万', '萬', '億', '亿', '兆', '零', '两', '兩',
		// The formal numerals used on cheques and contracts, traditional,
		// simplified and Japanese.
		'壹', '貳', '參', '肆', '伍', '陸', '柒', '捌', '玖', '拾', '佰', '仟',
		'贰', '叁', '陆', '壱', '弐', '参',
		// Twenty, thirty and forty as one character, and half.
		'廿', '卅', '卌', '半':
		return true
	}
	return false
}

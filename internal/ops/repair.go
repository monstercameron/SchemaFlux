package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The defining feature of a typed LLM library is: the parse fails, the error
// goes back to the model, the retry succeeds. This library retried transport
// errors only. A malformed body, a missing required field, or a category
// outside the allowed set was terminal — even though the retry machinery
// already existed in CallLLM and was simply not wired to what the answer said.
//
// A repair is not a retry. A retry sends the same request again and hopes the
// other end behaves differently. A repair sends a different request: the same
// task plus what was wrong with the last attempt.

// RepairPolicy governs how many times an operation may show the model its own
// mistake and ask again.
type RepairPolicy struct {
	// Attempts is the number of repair attempts after the first try. Zero means
	// no repair, which is the previous behaviour and remains the default for
	// operations that have not opted in.
	Attempts int
}

// DefaultRepairAttempts is what an operation uses when the caller has not said.
// One repair catches the overwhelmingly common case — a model that produced
// prose instead of JSON, or omitted a field — without turning one failed call
// into an unbounded spend.
const DefaultRepairAttempts = 1

// repairAttempts resolves the budget for a call.
func repairAttempts(policy RepairPolicy) int {
	if policy.Attempts > 0 {
		return policy.Attempts
	}
	if policy.Attempts < 0 {
		return 0
	}
	return config.GetRepairAttempts()
}

// RepairResult reports what a repaired call did, so a caller can tell a
// first-try success from a rescued one. A library that silently repairs is
// hiding the thing its operator most wants to measure.
type RepairResult struct {
	// Attempts is the number of provider calls made, including the first.
	Attempts int

	// Repaired is true when the answer came from a repair rather than the first
	// attempt.
	Repaired bool

	// Failures are the problems fed back to the model, in order.
	Failures []string
}

// withRepair runs an operation that produces a raw body and then validates it,
// feeding any validation failure back to the model and asking again.
//
// validate returns nil when the body is acceptable. Its error text is what the
// model is shown, so it has to describe the problem in terms the model can act
// on — "the response carried none of the expected fields; it has [x], wanted
// one of [y]" is useful, "invalid" is not.
func withRepair(
	ctx context.Context,
	systemPrompt, userPrompt string,
	opts types.OpOptions,
	policy RepairPolicy,
	validate func(body string) error,
) (string, RepairResult, error) {

	log := logger.GetLogger()
	budget := repairAttempts(policy)

	var result RepairResult
	var lastErr error

	for attempt := 0; attempt <= budget; attempt++ {
		prompt := userPrompt
		if attempt > 0 {
			prompt = repairPrompt(userPrompt, result.Failures)
		}

		body, err := callLLM(ctx, systemPrompt, prompt, opts)
		result.Attempts++

		if err != nil {
			// A transport failure is CallLLM's business; it has already
			// retried what is worth retrying. Repair is for answers, not for
			// the absence of one.
			return "", result, err
		}

		if validateErr := validate(body); validateErr != nil {
			lastErr = validateErr
			result.Failures = append(result.Failures, validateErr.Error())

			if attempt < budget {
				log.Debug("repairing an unusable answer",
					"attempt", result.Attempts,
					"remaining", budget-attempt,
					"problem", validateErr.Error(),
				)
				continue
			}
			return "", result, fmt.Errorf(
				"the answer was still unusable after %d attempts: %w", result.Attempts, validateErr)
		}

		result.Repaired = attempt > 0
		if result.Repaired {
			log.Info("an answer was repaired",
				"attempts", result.Attempts,
				"problems", strings.Join(result.Failures, "; "),
			)
		}
		return body, result, nil
	}

	return "", result, lastErr
}

// repairPrompt appends what went wrong to the original request. The original
// task stays first so the prefix is stable, which matters for prompt caching:
// a repair that rewrote the whole prompt would miss the cache every time.
func repairPrompt(original string, failures []string) string {
	var builder strings.Builder
	builder.WriteString(original)
	builder.WriteString("\n\nYour previous answer could not be used. ")

	if len(failures) == 1 {
		builder.WriteString("The problem was:\n")
		builder.WriteString(failures[0])
	} else {
		builder.WriteString("The problems were:\n")
		for i, failure := range failures {
			builder.WriteString(fmt.Sprintf("%d. %s\n", i+1, failure))
		}
	}

	builder.WriteString("\n\nReturn a corrected answer. Do not explain the correction; return only the answer.")
	return builder.String()
}

package ops

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Match runs the action of the first case whose condition holds, and reports
// whether any case ran. A provider failure while evaluating a semantic
// condition is returned rather than read as "did not match": treating an
// outage as a non-match routes every input to the default branch.
//
// The default case (Otherwise, "_", "default") is evaluated last regardless of
// where it appears in the argument list, so writing it first does not shadow
// the cases after it.
func Match(ctx context.Context, input any, cases ...types.Case) (bool, error) {
	if len(cases) == 0 {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var defaults []types.Case

	for _, c := range cases {
		if c.Action == nil {
			continue
		}

		if cond, ok := c.Condition.(string); ok && isDefaultCondition(cond) {
			defaults = append(defaults, c)
			continue
		}

		matched, err := caseMatches(ctx, input, c.Condition)
		if err != nil {
			return false, err
		}
		if matched {
			c.Action()
			return true, nil
		}
	}

	if len(defaults) > 0 {
		defaults[0].Action()
		return true, nil
	}

	return false, nil
}

// isDefaultCondition reports whether a string condition names the default
// branch rather than a pattern to evaluate.
func isDefaultCondition(condition string) bool {
	return condition == "_" || condition == "otherwise" || condition == "default"
}

// caseMatches evaluates one condition against the input.
func caseMatches(ctx context.Context, input any, condition any) (bool, error) {
	switch cond := condition.(type) {
	case string:
		return matchesStringCondition(ctx, input, cond)

	case reflect.Type:
		return matchesType(input, cond), nil

	case error:
		if err, ok := input.(error); ok {
			return reflect.TypeOf(err) == reflect.TypeOf(cond), nil
		}
		return false, nil

	default:
		inputType := reflect.TypeOf(input)
		condType := reflect.TypeOf(cond)
		return inputType != nil && condType != nil && inputType == condType, nil
	}
}

func When(condition any, action func()) types.Case {
	return types.Case{
		Condition: condition,
		Action:    action,
	}
}

func Like(template string, action func()) types.Case {
	return types.Case{
		Condition: template,
		Action:    action,
	}
}

func Otherwise(action func()) types.Case {
	return types.Case{
		Condition: "otherwise",
		Action:    action,
	}
}

func matchesStringCondition(ctx context.Context, input any, condition string) (bool, error) {
	if condition == "" {
		return false, nil
	}

	ctx, cancel := operationContext(ctx, 0)
	defer cancel()

	inputStr := fmt.Sprintf("%v", input)

	systemPrompt := `You are a pattern matching expert. Determine if the input matches the condition.

Rules:
- Consider semantic meaning
- Be consistent in matching
- Return ONLY "true" or "false"`

	userPrompt := fmt.Sprintf("Does this input:\n%s\n\nMatch this condition:\n%s", inputStr, condition)

	opt := types.OpOptions{
		Intelligence: types.Quick,
		Mode:         types.TransformMode,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		return false, fmt.Errorf("match: evaluating condition %q: %w", condition, err)
	}

	response = strings.ToLower(strings.TrimSpace(response))
	response = strings.Trim(response, "\"'.")

	switch response {
	case "true", "yes":
		return true, nil
	case "false", "no":
		return false, nil
	default:
		// An answer that is neither is not a "no". Saying so would route the
		// input to the default branch on the strength of a misread.
		return false, fmt.Errorf("match: condition %q got an answer that was neither true nor false", condition)
	}
}

func matchesType(input any, targetType reflect.Type) bool {
	if input == nil {
		return false
	}

	inputType := reflect.TypeOf(input)

	if inputType == targetType {
		return true
	}

	if targetType.Kind() == reflect.Interface {
		return inputType.Implements(targetType)
	}

	if inputType.Kind() == reflect.Ptr && targetType.Kind() == reflect.Ptr {
		return inputType.Elem() == targetType.Elem()
	}

	return false
}

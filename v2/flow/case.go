package flow

// A Case is one arm of a Match: a condition and the action that runs when it
// holds. Build one with When or Otherwise.
type Case struct {
	// Condition says when the case applies. A string is a description a model
	// judges the input against, except that the string "_", "otherwise" or
	// "default" is the default case and is not judged. A reflect.Type is
	// compared with the type of the input; an error value matches an input
	// error of the same type; any other value matches an input of the same
	// type.
	Condition any

	// Action is what runs when the case is the one that matches. A case with a
	// nil Action is skipped.
	Action func()
}

// When builds a Case that runs action when the input satisfies condition: a type,
// a value of a type, an error value, or a string that describes what the input
// should look like, which a model judges. It asks no model itself; Match does,
// when it reaches the case.
func When(condition any, action func()) Case {
	return Case{Condition: condition, Action: action}
}

// Otherwise builds the default case, which runs only when no other case matched,
// wherever it is written among the cases of a Match. Its Condition is the string
// "otherwise", which a Match does not judge.
func Otherwise(action func()) Case {
	return Case{Condition: "otherwise", Action: action}
}

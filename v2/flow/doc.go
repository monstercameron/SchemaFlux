// Package flow holds the combinators that compose steps and ask no model
// themselves: speculation, escalation, retry until a condition holds, voting,
// checkpoints, approval gates, map-reduce and guards. The control flow every
// program needs first (Branch, Select, Gather, Each) is in the root package; what
// is here is what a program needs when the plain forms are not enough.
//
// Two functions of the package do ask a model, and add a Record each. Decide takes
// one of several decisions: the first whose own condition holds, else the one a
// model chooses, with a decision marked as the fallback for a model that cannot be
// reached. Match runs the first of several cases whose condition holds, a
// condition that is a string being judged by a model and any other compared in
// Go; Case, When and Otherwise build its cases.
//
// A step is the root's Step: a function of a context that returns a value or an
// error. A flow of a decision, a branch and three steps at once is written with
// the root's functions:
//
//	triage, err := schemaflux.Assess[Triage](ctx, message)
//	if err != nil {
//		return err
//	}
//	reply, err := schemaflux.Branch(ctx, triage.Urgent, escalate, func(ctx context.Context) (Reply, error) {
//		parts, err := schemaflux.Gather(ctx, summarize, translate, tag)
//		...
//	})
//
// and the combinators of this package go where one step has to be raced,
// repeated, voted on or held for a person.
//
// # Errors
//
// A combinator's own failure is an *schemaflux.Error: a step that is nil is of kind
// ErrInvalidRequest, a step that panicked is of kind ErrInternal, and a context
// that is over is of kind ErrCanceled or ErrTimeout with the context's own error
// beneath it. An error returned by a function of the caller's (a step, a gate, a
// reconciler, a store, an operation over chunks) is returned as it was given, the
// same value, from every combinator and at every depth of nesting, so that
// err == errSentinel and errors.Is work as they would without the combinator. An
// error the library keeps in a result field is an *Error when it is the
// library's own and the caller's own when it is the caller's.
//
// # What each combinator does with a failure
//
// A step fails with an *Error of one of eighteen kinds, or with an error of the
// caller's own that carries none. Escalate, Until, Approve and Vote read the
// kind; Checkpoint, BranchAhead, BranchBoth and RouteAhead do not. Decide reads
// the kind of the failure of the model's call, where the decision marked as the
// fallback is what the stronger step is to Escalate. "Returns" means the step's
// own error is returned, the very value, and nothing more is run: no second
// step, no second attempt, no person asked, no fallback taken.
//
//	kind of the failure                        Escalate     Until          Approve                Vote                   Decide
//	ErrRateLimited, ErrUnavailable,
//	ErrTimeout (1)                             escalates    tries again    returns                failed sample          the fallback
//	ErrBusy, ErrTooLarge, ErrTruncated         escalates    tries again    returns                failed sample          returns
//	ErrInvalidAnswer                           escalates    tries again    asks for a review      failed sample          the fallback
//	                                                                       if recovery ran out,
//	                                                                       else returns
//	an error that carries no kind              escalates    tries again    returns                failed sample          the fallback
//	ErrConfiguration, ErrInvalidRequest,
//	ErrUnsupported, ErrUnauthorized, ErrPolicy,
//	ErrBudgetExceeded, ErrRefused, ErrClosed,
//	ErrReviewRequired                          returns      returns        returns                failed sample          returns
//	ErrCanceled                                returns      returns        returns                failed sample          returns
//	ErrInternal                                returns      returns        returns                failed sample,         returns
//	                                                                                              counted in
//	                                                                                              VoteRecord.Internal
//
// (1) When the caller's context is still live. A step's own timeout, or a
// deadline of its own, is no signal: it is read as an unavailable provider is.
// What stops every combinator is the caller's context having ended, cancelled or
// past its deadline, whatever the step returned: the context itself is checked
// before the next step, attempt or sample, and the *Error for the context is
// returned. Decide, which has no step, reads its failure the same way: a request
// of the model that timed out is the model being unavailable and takes the
// fallback, and a call whose caller's context has ended returns the error for it
// whatever the model said, and takes none.
//
// A defect, which is what ErrInternal is (a panic that was caught, a rule of the
// library that was broken), is never a reason to spend a stronger model on it, to
// run it again or to ask a person: none of them can repair it, so Approve returns
// it as it is and Escalate and Until stop at it. A cancellation is likewise never
// retried or escalated. Approve asks for a review when the step's error
// says that automated recovery ran out (an ErrInvalidAnswer made by repairs that
// failed), and for no other kind. A sample of a Vote that fails is a failed sample
// whatever its kind, and the vote goes on with the samples that succeeded;
// VoteRecord.Internal says how many of the failed were defects.
//
// Checkpoint returns the error of its step and keeps nothing, of any kind. The
// three speculative combinators return the error of the branch the decision
// chose, of any kind, and discard the error of the branch it did not choose. A
// caller's context that is done ends every combinator with the *Error for the
// context, before the next step or attempt is started.
//
// # Concurrency
//
// Each function says what it runs at once and what it waits for. The steps of
// BranchAhead, BranchBoth, RouteAhead, Vote and MapReduce run on goroutines of the
// combinator's own; the steps of Escalate, Until, Approve and Checkpoint run on the
// caller's goroutine, one after another. No combinator leaves a goroutine running
// when it returns. The requests the steps make are bounded by the Scheduler of the
// client on the context, whatever the combinator itself allows.
//
// None of these functions adds a Record but Decide and Match: only an operation
// that asks a model does, and the steps are the caller's. Their Records are
// "flow.decide" and "flow.match".
package flow

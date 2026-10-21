// Package chat is a conversation with a model that can ask for tools to be run.
//
// It is not an operation: it returns no typed value, and it is not an agent. The
// library never runs a tool and never loops by itself. The model asks, the
// caller's code runs the tool, and the caller gives the result back; this
// package keeps the history, checks it, and makes each turn one call of the
// library, with a Record, a data policy, a budget and a client like any other.
//
// # The loop is yours
//
// A Session is a value with a history. Send adds what the user said and returns
// the model's reply; when the reply asks for tools, run them and call
// SendResults, until it does not:
//
//	var s chat.Session
//	s.Instructions = "You answer questions about the weather. Use get_weather."
//	s.Tools = []schemaflux.Tool{weather}
//	reply, err := s.Send(ctx, "How warm is Oslo?")
//	for err == nil && len(reply.ToolCalls) > 0 {
//		results := map[string]string{}
//		for _, c := range reply.ToolCalls {
//			results[c.ID] = lookup(c)
//		}
//		reply, err = s.SendResults(ctx, results)
//	}
//	fmt.Println(reply.Content)
//
// A reply is a [schemaflux.Message], which is what History holds. On failure a
// turn returns the zero Message and an *Error, and History is unchanged: there is
// no partial turn.
//
// # What a turn checks
//
// Before anything is sent the history is checked, whole, on every call, so a
// history a caller saved, resumed or cut to fit the context window is held to
// the same rules as one the session built (see Session.History). After the reply
// comes back it is checked in Go: every call names a declared tool, has an id
// that no earlier call has, and has arguments that are a JSON object; a reply
// with neither words nor calls is refused. A reply that is only tool calls is a
// complete answer. A reply that fails a check is ErrInvalidAnswer and is not kept.
//
// # What a turn does not do
//
// A turn does not repair a reply, retry what the client's Retries do not retry,
// stream, or choose a tool for the model. The delivered contract of a turn is
// ContractPromptOnly: the model's text is its own, so a client whose data policy
// requires more refuses the turn before anything is sent. A provider that cannot
// take tools or a message list refuses with ErrUnsupported before anything is
// sent (among the built-in ones the Anthropic, local and decision providers are
// such), and a program that wants to fall back from one to a provider that can
// does so with mw.Fallback.
//
// Neither the text of a message, nor an instruction, nor a tool's arguments or
// result ever appears in an error or a Record: a Record says how many calls and
// how many tokens, never which.
package chat

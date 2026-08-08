package testfixtures

import "encoding/json"

// ResponsesMessage builds a Responses API body carrying a reasoning item before
// the message item, which is the shape a reasoning model returns.
//
// The ordering is the point: a parser that takes the first output item finds
// reasoning rather than the answer, so a fixture that emitted only a message
// would pass against a parser that is wrong about real traffic.
func ResponsesMessage(answer string) string {
	payload, _ := json.Marshal(answer)
	return `{
	  "id":"resp","status":"completed","model":"gpt-5.6-luna",
	  "output":[
	    {"type":"reasoning","content":[{"type":"reasoning_text","text":"Let me work through this step by step."}]},
	    {"type":"message","content":[{"type":"output_text","text":` + string(payload) + `}]}
	  ],
	  "usage":{"input_tokens":50,"output_tokens":20,"total_tokens":70,
	           "input_tokens_details":{"cached_tokens":32},
	           "output_tokens_details":{"reasoning_tokens":12}}
	}`
}

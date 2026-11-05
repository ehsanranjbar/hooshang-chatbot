package llm

import (
	"context"
	"fmt"
)

// ToolFunc executes one tool. arguments is the JSON the model passed; the
// result goes back to the model as a role:"tool" message.
type ToolFunc func(ctx context.Context, arguments string) (string, error)

// RunTools runs a chat request, executes every tool call the model asks for
// (via handlers, keyed by tool name), feeds the results back, and repeats
// until the model answers without tool calls. At most maxRounds tool rounds
// run; when they are exhausted and the model still wants tools, one final
// Chat is made without tools and its response returned. A call to an unknown
// tool, or a handler error, becomes the tool result "error: …"; it does not
// abort the loop.
func RunTools(ctx context.Context, c Client, req Request, handlers map[string]ToolFunc, maxRounds int) (Response, error) {
	req.Messages = append([]Message(nil), req.Messages...)
	for round := 0; ; round++ {
		if round >= maxRounds {
			// Rounds exhausted and the model still wants tools; ask once
			// more without them for a final text answer.
			req.Tools = nil
			resp, err := c.Chat(ctx, req)
			return resp, err
		}
		resp, err := c.Chat(ctx, req)
		if err != nil {
			return Response{}, err
		}
		if len(resp.ToolCalls) == 0 {
			return resp, nil
		}
		req.Messages = append(req.Messages, Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, tc := range resp.ToolCalls {
			h, ok := handlers[tc.Name]
			var result string
			if !ok {
				result = fmt.Sprintf("error: unknown tool %s", tc.Name)
			} else if out, err := h(ctx, tc.Arguments); err != nil {
				result = "error: " + err.Error()
			} else {
				result = out
			}
			req.Messages = append(req.Messages, Message{Role: "tool", ToolCallID: tc.ID, Content: result})
		}
	}
}

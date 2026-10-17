package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Next makes exactly one model request from a session snapshot. It returns
// one Event per native output item, in order, without modifying s. It doesn't
// add user input, execute tools, persist results, select a branch, or continue
// automatically. Completion can request tools rather than finish the user's task.
//
// The returned slice is one complete output batch. Persist it as an ordered batch
// under the history position captured before the request, even if navigation has
// since changed. Don't branch into a partial generated batch. If saving fails
// after Next succeeds, the caller can save those events without new inference.
//
// The API validates request settings, tool declarations, and call/result history.
// Next only converts supported events to and from the wire format. Messages and
// tool results support images as URLs or image data URLs. Retain Native payloads
// unchanged for replay.
//
// # Context and compaction
//
// Each request sends the required history as input with stream=true and
// store=false. SSE is private; there is no server-stored conversation to resume.
// Instructions and current tool schemas are supplied separately on every request.
// Reasoning summaries are always requested with summary="detailed", but aren't
// guaranteed to be returned.
//
// When CompactThreshold is nonzero, Next requests native compaction. Choose a
// positive token threshold appropriate for the model; zero disables new
// compactions. Context starts at the latest compaction item on the selected
// branch and includes every item after it, including items in the same batch.
// Earlier Events aren't changed or deleted. Compaction is opaque, not plaintext.
// SIWC/model support for native compaction hasn't been verified here; unsupported
// requests return errors without retries or custom-summary fallback.
//
// # Failure and cancellation
//
// Failures, incomplete responses, malformed or interrupted streams, and output
// that can't be converted return an error and a nil slice, never partial history.
// A plan-usage failure can arrive after HTTP 200 and output deltas. Next neither
// retries nor resumes an interrupted generation.
//
// ctx controls credential renewal and the whole HTTP/SSE operation. For a Stop
// action, create a per-request context and keep its cancel function available:
//
//	requestCtx, cancel := context.WithCancel(ctx)
//	defer cancel()
//
//	// The Stop handler can call cancel from another goroutine.
//	events, err := client.Next(requestCtx, s)
//
// Cancellation stops the local request/stream, but doesn't guarantee immediate
// server-side termination or undo work already performed.
//
// See https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations
// and https://developers.openai.com/api/docs/guides/compaction.
func (c *Client) Next(ctx context.Context, s Session) ([]Event, error) {
	// Native compaction replaces model context, never stored history. Convert
	// only the latest compaction item and the events that follow it.
	contextStart := 0
	for i := len(s.Events) - 1; i >= 0; i-- {
		if s.Events[i].Type == EventCompaction {
			contextStart = i
			break
		}
	}
	input := make([]any, 0, len(s.Events)-contextStart)
	for i, event := range s.Events[contextStart:] {
		if len(event.Native) > 0 {
			// Original items preserve phases, encrypted reasoning, compaction,
			// and fields that the public display view doesn't interpret.
			input = append(input, event.Native)
			continue
		}
		parts := make([]any, 0, 1+len(event.Images))
		if event.Text != "" {
			parts = append(parts, map[string]any{"type": "input_text", "text": event.Text})
		}
		for _, image := range event.Images {
			parts = append(parts, map[string]any{"type": "input_image", "image_url": image})
		}

		var item map[string]any
		switch event.Type {
		case EventMessage:
			item = map[string]any{
				"type":    EventMessage,
				"role":    event.Role,
				"content": parts,
			}
		case EventFunctionCallOutput:
			var output any = event.Text
			if len(event.Images) > 0 {
				output = parts
			}
			item = map[string]any{
				"type":    EventFunctionCallOutput,
				"call_id": event.ToolCallID,
				"output":  output,
			}
		default:
			return nil, fmt.Errorf("event %d: %s requires its original native item", contextStart+i, event.Type)
		}
		input = append(input, item)
	}

	request := map[string]any{
		"model":        s.Model,
		"instructions": s.Instructions,
		"input":        input,
	}
	reasoning := map[string]any{"summary": "detailed"}
	if s.Effort != "" {
		reasoning["effort"] = s.Effort
	}
	request["reasoning"] = reasoning
	if s.CompactThreshold != 0 {
		request["context_management"] = []any{map[string]any{
			"type":              EventCompaction,
			"compact_threshold": s.CompactThreshold,
		}}
	}
	if len(s.Tools) > 0 {
		functions := make([]any, 0, len(s.Tools))
		for _, schema := range s.Tools {
			// Don't modify the caller's schema.
			function := make(map[string]any, len(schema)+1)
			for key, value := range schema {
				function[key] = value
			}
			function["type"] = "function"
			functions = append(functions, function)
		}
		request["tools"] = []any{map[string]any{
			"type":        "namespace",
			"name":        "functions",
			"description": "Tools available to the assistant.",
			"tools":       functions,
		}}
	}

	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := c.responses(ctx, request, &response); err != nil {
		return nil, err
	}

	createdAt := time.Now()
	events := make([]Event, 0, len(response.Output))
	for _, fields := range response.Output {
		// created_by is output-only and must not be replayed as input.
		delete(fields, "created_by")
		raw, err := json.Marshal(fields)
		if err != nil {
			return nil, fmt.Errorf("encoding replay item: %w", err)
		}
		var item struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Phase     string          `json:"phase"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments string          `json:"arguments"`
			Output    json.RawMessage `json:"output"`
			Content   []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("decoding output item: %w", err)
		}
		event := Event{
			CreatedAt:  createdAt,
			Type:       item.Type,
			Role:       item.Role,
			Phase:      item.Phase,
			ToolCallID: item.CallID,
			ToolName:   item.Name,
			Native:     raw,
		}
		switch item.Type {
		case EventMessage:
			texts := make([]string, 0, len(item.Content))
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					texts = append(texts, part.Text)
				case "refusal":
					texts = append(texts, part.Refusal)
				default:
					return nil, fmt.Errorf("unsupported message content type %q", part.Type)
				}
			}
			event.Text = strings.Join(texts, "\n\n")
		case EventFunctionCall:
			dec := json.NewDecoder(strings.NewReader(item.Arguments))
			dec.UseNumber()
			if err := dec.Decode(&event.ToolArguments); err != nil {
				return nil, fmt.Errorf("decoding arguments for %s: %w", item.Name, err)
			}
			event.Text = item.Name + "(" + item.Arguments + ")"
		case EventFunctionCallOutput:
			// The native output is a union: text or an array of content parts.
			if err := json.Unmarshal(item.Output, &event.Text); err != nil {
				var parts []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL string `json:"image_url"`
				}
				if err := json.Unmarshal(item.Output, &parts); err != nil {
					return nil, fmt.Errorf("decoding function output: %w", err)
				}
				var texts []string
				for _, part := range parts {
					switch part.Type {
					case "input_text":
						texts = append(texts, part.Text)
					case "input_image":
						event.Images = append(event.Images, part.ImageURL)
					default:
						return nil, fmt.Errorf("unsupported function output content type %q", part.Type)
					}
				}
				event.Text = strings.Join(texts, "\n\n")
			}
		case EventReasoning:
			var summaries []string
			for _, summary := range item.Summary {
				if summary.Text != "" {
					summaries = append(summaries, summary.Text)
				}
			}
			event.Text = strings.Join(summaries, "\n\n")
			if event.Text == "" {
				event.Text = "Model reasoning."
			}
		case EventCompaction:
			event.Text = "Context compacted."
		default:
			return nil, fmt.Errorf("unsupported output item type %q", item.Type)
		}
		events = append(events, event)
	}
	return events, nil
}

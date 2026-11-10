package provider

import (
	"bytes"
	"context"
	crand "crypto/rand"
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Terminal API failures returned by Prompt support errors.Is. Usage-limit
// failures may be handled internally when a fallback token is available.
var (
	ErrQuotaExceeded         = errors.New("API quota exceeded")
	ErrUsageLimitReached     = errors.New("account usage limit reached")
	ErrAuthentication        = errors.New("authentication failed")
	ErrPermissionDenied      = errors.New("permission denied")
	ErrInvalidRequest        = errors.New("invalid request")
	ErrContextLengthExceeded = errors.New("context length exceeded")
	ErrResponseIncomplete    = errors.New("response incomplete")
)

// Function exposes a local tool to the model. Calls in one response run
// concurrently; implementations must honor cancellation, synchronize shared
// resources, and enforce their own execution permissions.
type Function interface {
	// Schema returns a strict Responses API function definition.
	Schema() map[string]any
	// Call receives decoded JSON arguments; numbers use encoding/json.Number.
	// Return tool errors as text parts.
	Call(context.Context, map[string]any) []Part
}

// Session holds conversation settings and native Responses API events. Use one
// Prompt call at a time per session. The caller owns session persistence.
type Session struct {
	ID               string           // Local identifier used in logs.
	Model            string           // Responses API model name.
	Effort           string           // Model reasoning effort.
	Instructions     string           // Instructions sent on every request.
	CompactThreshold int              // Native compaction token threshold.
	ServiceTier      string           // Requested API service tier, sent unchanged.
	Functions        []Function       // Locally executed functions.
	Events           []map[string]any // Full history, including native opaque events.
}

// NewSession creates an in-memory conversation with a 128000-token compaction
// threshold and the "default" service tier.
func NewSession(model, effort, instructions string, functions []Function) *Session {
	return &Session{
		ID:               crand.Text(),
		Model:            model,
		Effort:           effort,
		Instructions:     instructions,
		CompactThreshold: 128000,
		ServiceTier:      "default",
		Functions:        slices.Clone(functions),
	}
}

// Prompt appends a UTF-8 user message, runs the model and local tools, and returns
// the final assistant text without reasoning or intermediate tool output. ctx
// governs the entire operation, including authentication and automatic retries.
// Each concurrent function batch has a 30-minute timeout, bounded by ctx.
//
// On failure after prompt validation, the user message and completed events remain
// in s. Another Prompt starts a new turn; it does not retry the failed one.
// Automatic retries do not repeat completed function calls.
func (c *Client) Prompt(ctx context.Context, s *Session, prompt string) (string, error) {
	if !utf8.ValidString(prompt) {
		return "", errors.New("prompt has invalid utf-8")
	}
	c.appendEvent(s, map[string]any{"type": "message", "role": "user", "content": prompt})

	for {
		inputEvents, err := c.responses(ctx, s)
		if err != nil {
			return "", err
		}
		c.appendEvent(s, inputEvents...)

		var functionCalls []map[string]any
		var lastMessage map[string]any
		for _, e := range inputEvents {
			if t, _ := e["type"].(string); t == "function_call" {
				functionCalls = append(functionCalls, e)
			} else if t == "message" {
				if phase, _ := e["phase"].(string); phase != "commentary" {
					lastMessage = e
				}
			}
		}
		if len(functionCalls) == 0 {
			var textParts []string
			for _, part := range decodeParts(lastMessage["content"]) {
				if part.ContentType == "text/plain" {
					textParts = append(textParts, string(part.Content))
				}
			}
			return strings.Join(textParts, "\n\n"), nil
		}
		turnCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		outputEvents := make([]map[string]any, len(functionCalls))
		var wg sync.WaitGroup
		for index, e := range functionCalls {
			callID, _ := e["call_id"].(string)
			name, _ := e["name"].(string)
			arguments, _ := e["arguments"].(string)
			outputEvents[index] = map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
			}
			args, err := decodeArguments(arguments)
			if err != nil {
				outputEvents[index]["output"] = fmt.Sprintf("invalid arguments: %s", err)
				continue
			}
			found := false
			for _, f := range s.Functions {
				if fName, _ := f.Schema()["name"].(string); fName == name {
					wg.Go(func() {
						outputEvents[index]["output"] = encodeParts(f.Call(turnCtx, args))
					})
					found = true
					break
				}
			}
			if !found {
				outputEvents[index]["output"] = fmt.Sprintf("function %q not found", name)
			}
		}
		wg.Wait()
		cancel()
		c.appendEvent(s, outputEvents...)
	}
}

func decodeArguments(arguments string) (map[string]any, error) {
	// Retain v2's strict validation of a single JSON value, duplicate names,
	// and Unicode before using the v1 decoder for its UseNumber support.
	var raw jsonv1.RawMessage
	if err := json.Unmarshal([]byte(arguments), &raw); err != nil {
		return nil, err
	}
	dec := jsonv1.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		return nil, err
	}
	if args == nil {
		return nil, errors.New("expected a JSON object")
	}
	return args, nil
}

func (c *Client) appendEvent(s *Session, es ...map[string]any) {
	for _, e := range es {
		s.Events = append(s.Events, e)
		if c.Logger != nil {
			if b, err := json.Marshal(e); err == nil {
				c.Logger.Printf("session=%s model=%q service_tier=%q event=%q\n", s.ID, s.Model, s.ServiceTier, string(b))
			}
		}
	}
}

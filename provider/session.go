package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ceraso.dev/bobo/omap"
)

type Tool interface {
	Schema() map[string]any
	Call(context.Context, string) []Part
}

type ToolEntry struct {
	Type   string
	Name   string
	Schema map[string]any
	Call   func(context.Context, string) []Part
}

func HandleCalls(ctx context.Context, tools omap.Map[string, ToolEntry], inputEvents []Event) []Event {
	type callU struct {
		f *FunctionCall
		t *ToolCall
	}
	var calls omap.Map[string, callU]
	for _, ev := range inputEvents {
		if functionCall := ev.FunctionCall(); functionCall != nil {
			calls.Set(functionCall.ID, callU{f: functionCall})
		} else if functionCallOutput := ev.FunctionCallOutput(); functionCallOutput != nil {
			calls.Delete(functionCallOutput.ID)
		} else if toolCall := ev.ToolCall(); toolCall != nil {
			calls.Set(toolCall.ID, callU{t: toolCall})
		} else if toolCallOutput := ev.ToolCallOutput(); toolCallOutput != nil {
			calls.Delete(toolCallOutput.ID)
		}
	}

	outputEvents := make([]Event, calls.Len())
	i := 0
	var wg sync.WaitGroup
	for _, call := range calls.All() {
		index := i
		if call.f != nil {
			if tool, ok := tools.Get(call.f.Name); ok && tool.Type == "function" {
				wg.Go(func() {
					parts := tool.Call(ctx, call.f.Arguments)
					outputEvents[index] = FunctionCallOutput{ID: call.f.ID, Output: parts}.Event()
				})
			} else {
				outputEvents[index] = FunctionCallOutput{ID: call.f.ID, Output: []Part{{"text/plain", []byte(fmt.Sprintf("error: function '%s' not found", call.f.Name))}}}.Event()
			}
		} else if call.t != nil {
			if tool, ok := tools.Get(call.t.Name); ok && tool.Type == "custom" {
				wg.Go(func() {
					parts := tool.Call(ctx, call.t.Input)
					outputEvents[index] = ToolCallOutput{ID: call.t.ID, Output: parts}.Event()
				})
			} else {
				outputEvents[index] = ToolCallOutput{ID: call.t.ID, Output: []Part{{"text/plain", []byte(fmt.Sprintf("error: custom tool '%s' not found", call.t.Name))}}}.Event()
			}
		}
		i++
	}
	wg.Wait()
	return outputEvents
}

type Session struct {
	Model            string
	Effort           string
	Instructions     string
	CompactThreshold int
	ServiceTier      string
	Tools            omap.Map[string, ToolEntry]
	Events           []Event
	OnEvent          func(Event) error
}

func NewSession(model, effort, instructions string, tools []Tool) *Session {
	var toolsMap omap.Map[string, ToolEntry]
	for _, tool := range tools {
		toolSchema := tool.Schema()
		toolType, _ := toolSchema["type"].(string)
		toolName, _ := toolSchema["name"].(string)
		toolsMap.Set(toolName, ToolEntry{Type: toolType, Name: toolName, Schema: toolSchema, Call: tool.Call})
	}
	return &Session{
		Model:            model,
		Effort:           effort,
		Instructions:     instructions,
		CompactThreshold: 128000,
		ServiceTier:      "auto",
		Tools:            toolsMap,
	}
}

func (s *Session) AppendEvent(ev Event) error {
	if s.OnEvent != nil {
		if err := s.OnEvent(ev); err != nil {
			return err
		}
	}
	s.Events = append(s.Events, ev)
	return nil
}

func (c *Client) Prompt(ctx context.Context, s *Session, prompt string) (string, error) {
	if !utf8.ValidString(prompt) {
		return "", errors.New("prompt has invalid utf-8")
	}
	for _, ev := range HandleCalls(ctx, s.Tools, s.Events) {
		if err := s.AppendEvent(ev); err != nil {
			return "", err
		}
	}
	if err := s.AppendEvent(Message{Role: "user", Content: []Part{{"text/plain", []byte(prompt)}}}.Event()); err != nil {
		return "", err
	}
	for {
		inputEvents, err := c.Next(ctx, s)
		if err != nil {
			return "", err
		}
		last := true
		var lastMessage *Message
		for _, ev := range inputEvents {
			if err := s.AppendEvent(ev); err != nil {
				return "", err
			}
			if f := ev.FunctionCall(); f != nil {
				last = false
			} else if t := ev.ToolCall(); t != nil {
				last = false
			} else if m := ev.Message(); m != nil && m.Phase != "commentary" {
				lastMessage = m
			}
		}
		if last {
			if lastMessage != nil {
				var textParts []string
				for _, part := range lastMessage.Content {
					if part.ContentType == "text/plain" {
						textParts = append(textParts, string(part.Content))
					}
				}
				return strings.Join(textParts, "\n\n"), nil
			}
			return "", nil
		}
		turnCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		outputEvents := HandleCalls(turnCtx, s.Tools, inputEvents)
		for _, ev := range outputEvents {
			if err := s.AppendEvent(ev); err != nil {
				return "", err
			}
		}
	}
}

func (c *Client) Compact(ctx context.Context, s *Session) error {
	s.Events = append(s.Events, Event{body: map[string]any{"type": "compaction_trigger"}})
	evs, err := c.Next(ctx, s)
	s.Events = s.Events[:len(s.Events)-1]
	if err != nil {
		return err
	}
	for _, ev := range evs {
		if err := s.AppendEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) Next(ctx context.Context, s *Session) ([]Event, error) {
	reqObj := map[string]any{
		"instructions": s.Instructions,
		"model":        s.Model,
		"reasoning": map[string]any{
			"summary": "detailed",
			"effort":  s.Effort,
		},
		"context_management": []map[string]any{{
			"type":              "compaction",
			"compact_threshold": s.CompactThreshold,
		}},
		"service_tier": s.ServiceTier,
		"store":        false,
		"stream":       true,
	}
	tools := []map[string]any{}
	for _, t := range s.Tools.All() {
		tools = append(tools, t.Schema)
	}
	reqObj["tools"] = []map[string]any{
		{
			"type":        "namespace",
			"name":        "main",
			"description": "Main tools.",
			"tools":       tools,
		},
		{
			"type": "web_search",
		},
	}
	startIndex := 0
	for i := len(s.Events) - 1; i >= 0; i-- {
		if c := s.Events[i].Compaction(); c != nil {
			startIndex = i
			break
		}
	}
	input := make([]map[string]any, len(s.Events)-startIndex)
	for i := startIndex; i < len(s.Events); i++ {
		input[i-startIndex] = s.Events[i].body
	}
	reqObj["input"] = input
	reqBody, err := json.Marshal(reqObj)
	if err != nil {
		return nil, fmt.Errorf("encoding request object: %w", err)
	}
	var resObj struct {
		Output []map[string]any `json:"output"`
	}

	token, err := c.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting client access token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/responses", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("constructing HTTP request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	res, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doing request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		resBody, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, fmt.Errorf("reading response error body (%s): %w", res.Status, err)
		}
		return nil, fmt.Errorf("creating response: %s: %s", res.Status, resBody)
	}

	reader := bufio.NewReader(res.Body)
	var data []byte
SSE_LOOP:
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("response stream ended before completion: %w", io.ErrUnexpectedEOF)
			}
			return nil, fmt.Errorf("reading response stream: %w", err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(line[len("data:"):], " ")...)
			data = append(data, '\n')
			continue
		}
		if line != "" || len(data) == 0 {
			continue
		}

		var event struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Code     string          `json:"code"`
			Message  string          `json:"message"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, fmt.Errorf("decoding response event: %w", err)
		}
		data = data[:0]

		switch event.Type {
		case "response.completed":
			if err := json.Unmarshal(event.Response, &resObj); err != nil {
				return nil, fmt.Errorf("decoding completed response: %w", err)
			}
			break SSE_LOOP
		case "response.failed", "response.incomplete":
			var failure struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
				IncompleteDetails struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
			}
			if err := json.Unmarshal(event.Response, &failure); err != nil {
				return nil, fmt.Errorf("decoding failed response: %w", err)
			}
			if event.Type == "response.incomplete" {
				return nil, fmt.Errorf("response incomplete: %s", failure.IncompleteDetails.Reason)
			}
			return nil, fmt.Errorf("response failed: %s: %s", failure.Error.Code, failure.Error.Message)
		case "error":
			return nil, fmt.Errorf("response stream error: %s: %s", event.Code, event.Message)
		}
	}

	var events []Event
	for _, output := range resObj.Output {
		events = append(events, Event{body: output})
	}
	return events, nil
}

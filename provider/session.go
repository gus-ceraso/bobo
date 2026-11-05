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
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Session struct {
	Model            string
	Effort           string
	Instructions     string
	CompactThreshold int
	ServiceTier      string
	Tools            []Tool
	Events           []Event
	OnEvent          func(Event)
}

type Tool interface {
	Schema() map[string]any
	Call(context.Context, string) []Part
}

func NewSession(model, effort, instructions string, tools []Tool) *Session {
	return &Session{
		Model:            model,
		Effort:           effort,
		Instructions:     instructions,
		CompactThreshold: 128000,
		ServiceTier:      "auto",
		Tools:            slices.Clone(tools),
	}
}

func (s *Session) PushEvent(ev Event) {
	s.Events = append(s.Events, ev)
	if s.OnEvent != nil {
		s.OnEvent(ev)
	}
}

func (s *Session) PopEvent() {
	if len(s.Events) > 0 {
		s.Events = s.Events[:len(s.Events)-1]
	}
}

func (c *Client) Prompt(ctx context.Context, s *Session, prompt string) (string, int, error) {
	if !utf8.ValidString(prompt) {
		return "", 0, errors.New("prompt has invalid utf-8")
	}
	s.PushEvent(Message{Role: "user", Content: []Part{{"text/plain", []byte(prompt)}}}.Event())
	for {
		inputEvents, tokens, err := c.Next(ctx, s)
		if err != nil {
			return "", 0, err
		}
		deadline := time.Now().Add(5 * time.Minute)
		var functionCalls []*FunctionCall
		var toolCalls []*ToolCall
		var lastMessage *Message
		for _, ev := range inputEvents {
			s.PushEvent(ev)
			if f := ev.FunctionCall(); f != nil {
				functionCalls = append(functionCalls, f)
			} else if t := ev.ToolCall(); t != nil {
				toolCalls = append(toolCalls, t)
			} else if m := ev.Message(); m != nil && m.Phase != "commentary" {
				lastMessage = m
			}
		}
		if len(functionCalls) == 0 && len(toolCalls) == 0 {
			if lastMessage != nil {
				var textParts []string
				for _, part := range lastMessage.Content {
					if part.ContentType == "text/plain" {
						textParts = append(textParts, string(part.Content))
					}
				}
				return strings.Join(textParts, "\n\n"), tokens, nil
			}
			return "", tokens, nil
		}

		outputEvents := make([]Event, len(functionCalls)+len(toolCalls))
		var wg sync.WaitGroup
		turnCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		for i, functionCall := range functionCalls {
			found := false
			for _, tool := range s.Tools {
				toolSchema := tool.Schema()
				toolType, _ := toolSchema["type"].(string)
				toolName, _ := toolSchema["name"].(string)
				if toolType == "function" && toolName == functionCall.Name {
					found = true
					wg.Go(func() {
						parts := tool.Call(turnCtx, functionCall.Arguments)
						outputEvents[i] = FunctionCallOutput{ID: functionCall.ID, Output: parts}.Event()
					})
					break
				}
			}
			if !found {
				outputEvents[i] = FunctionCallOutput{ID: functionCall.ID, Output: []Part{{"text/plain", []byte(fmt.Sprintf("%s: function '%s' not found", functionCall.ID, functionCall.Name))}}}.Event()
			}
		}
		for i, toolCall := range toolCalls {
			found := false
			for _, tool := range s.Tools {
				toolSchema := tool.Schema()
				toolType, _ := toolSchema["type"].(string)
				toolName, _ := toolSchema["name"].(string)
				if toolType == "custom" && toolName == toolCall.Name {
					found = true
					wg.Go(func() {
						parts := tool.Call(turnCtx, toolCall.Input)
						outputEvents[len(functionCalls)+i] = ToolCallOutput{ID: toolCall.ID, Output: parts}.Event()
					})
					break
				}
			}
			if !found {
				outputEvents[len(functionCalls)+i] = ToolCallOutput{ID: toolCall.ID, Output: []Part{{"text/plain", []byte(fmt.Sprintf("%s: tool '%s' not found", toolCall.ID, toolCall.Name))}}}.Event()
			}
		}
		wg.Wait()
		for _, ev := range outputEvents {
			s.PushEvent(ev)
		}
	}
}

func (c *Client) Compact(ctx context.Context, s *Session) error {
	s.Events = append(s.Events, Event{body: map[string]any{"type": "compaction_trigger"}})
	evs, _, err := c.Next(ctx, s)
	s.Events = s.Events[:len(s.Events)-1]
	s.PopEvent()
	if err != nil {
		return err
	}
	for _, ev := range evs {
		s.PushEvent(ev)
	}
	return nil
}

func (c *Client) Next(ctx context.Context, s *Session) ([]Event, int, error) {
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
	for _, t := range s.Tools {
		tools = append(tools, t.Schema())
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
	var startIndex int
	for i := len(s.Events)-1; i >= 0; i-- {
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
		return nil, 0, fmt.Errorf("encoding request object: %w", err)
	}
	var resObj struct {
		Output []map[string]any `json:"output"`
		Usage  struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}

	token, err := c.Token(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("getting client access token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/responses", bytes.NewReader(reqBody))
	if err != nil {
		return nil, 0, fmt.Errorf("constructing HTTP request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	res, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("doing request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		resBody, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, 0, fmt.Errorf("reading response error body (%s): %w", res.Status, err)
		}
		return nil, 0, fmt.Errorf("creating response: %s: %s", res.Status, resBody)
	}

	reader := bufio.NewReader(res.Body)
	var data []byte
SSE_LOOP:
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, 0, fmt.Errorf("response stream ended before completion: %w", io.ErrUnexpectedEOF)
			}
			return nil, 0, fmt.Errorf("reading response stream: %w", err)
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
			return nil, 0, fmt.Errorf("decoding response event: %w", err)
		}
		data = data[:0]

		switch event.Type {
		case "response.completed":
			if err := json.Unmarshal(event.Response, &resObj); err != nil {
				return nil, 0, fmt.Errorf("decoding completed response: %w", err)
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
				return nil, 0, fmt.Errorf("decoding failed response: %w", err)
			}
			if event.Type == "response.incomplete" {
				return nil, 0, fmt.Errorf("response incomplete: %s", failure.IncompleteDetails.Reason)
			}
			return nil, 0, fmt.Errorf("response failed: %s: %s", failure.Error.Code, failure.Error.Message)
		case "error":
			return nil, 0, fmt.Errorf("response stream error: %s: %s", event.Code, event.Message)
		}
	}

	var events []Event
	for _, output := range resObj.Output {
		events = append(events, Event{body: output})
	}
	return events, resObj.Usage.TotalTokens, nil
}

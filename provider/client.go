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
)

type Client struct {
	Token func(context.Context) (string, error)
}

type Config struct {
	Model            string           `json:"model"`
	Effort           string           `json:"effort"`
	Instructions     string           `json:"instructions"`
	CompactThreshold int              `json:"compact_threshold"`
	ServiceTier      string           `json:"service_tier"`
	Tools            []map[string]any `json:"tools"`
}

func (c *Client) Next(ctx context.Context, cfg *Config, evs []Event) ([]Event, error) {
	reasoning := map[string]any{"summary": "detailed"}
	if cfg.Effort != "" {
		reasoning["effort"] = cfg.Effort
	}
	reqObj := map[string]any{
		"instructions": cfg.Instructions,
		"model":        cfg.Model,
		"reasoning":    reasoning,
		"store":        false,
		"stream":       true,
	}
	if cfg.CompactThreshold != 0 {
		reqObj["context_management"] = []any{map[string]any{
			"type":              "compaction",
			"compact_threshold": cfg.CompactThreshold,
		}}
	}
	if cfg.ServiceTier != "" {
		reqObj["service_tier"] = cfg.ServiceTier
	}
	if len(cfg.Tools) > 0 {
		reqObj["tools"] = []any{map[string]any{
			"type":        "namespace",
			"name":        "main",
			"description": "Main tools available to the assistant.",
			"tools":       cfg.Tools,
		}}
	}
	input := make([]map[string]any, 0, len(evs))
	for _, e := range evs {
		input = append(input, e.body)
	}
	reqObj["input"] = input
	reqBody, err := json.Marshal(reqObj)
	if err != nil {
		return nil, fmt.Errorf("encoding request object: %w", err)
	}
	var resObj struct{ Output []map[string]any }

	token, err := c.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting client access token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/responses", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("constructing HTTP request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	res, err := http.DefaultClient.Do(req)
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

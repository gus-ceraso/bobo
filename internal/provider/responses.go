package provider

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type responseError struct {
	Code    string            `json:"code"`
	Type    string            `json:"type"`
	Headers map[string]string `json:"headers"`
}

type responseFailure struct {
	status     int
	headers    http.Header
	receivedAt time.Time
	detail     responseError
	diagnostic string
}

// responses serializes one request and retries attempts without committing any
// partial output. Authentication is reread for every attempt, including fallback.
func (c *Client) responses(ctx context.Context, s *Session) ([]map[string]any, error) {
	body, err := responseBody(s)
	if err != nil {
		return nil, fmt.Errorf("encoding request object: %w", err)
	}
	delay := 500 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		auth, err := c.accessToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("getting client access token: %w", err)
		}
		output, failure, err := c.responseAttempt(ctx, body, auth)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		if failure == nil {
			return output, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		kind, retryable := classifyResponseFailure(failure.status, failure.detail)
		if kind == ErrUsageLimitReached {
			retry, err := c.usageFallback(ctx, auth)
			if err != nil {
				return nil, fmt.Errorf("selecting fallback credential: %w", err)
			}
			if retry {
				continue
			}
		}
		if kind != nil {
			return nil, fmt.Errorf("%w: %s", kind, failure.diagnostic)
		}
		retryAt, retry := responseRetry(failure, retryable)
		if !retry {
			return nil, errors.New(failure.diagnostic)
		}
		if err := waitResponseRetry(ctx, delay, retryAt); err != nil {
			return nil, err
		}
		delay = min(delay*2, 30*time.Second)
	}
}

// responseAttempt owns the HTTP body, so all exits close it before retrying.
func (c *Client) responseAttempt(ctx context.Context, body []byte, auth requestAuth) ([]map[string]any, *responseFailure, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("constructing HTTP request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	res, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("doing request: %w", err)
	}
	defer res.Body.Close()
	receivedAt := time.Now()
	if res.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		failure := &responseFailure{
			status: res.StatusCode, headers: res.Header.Clone(), receivedAt: receivedAt,
			diagnostic: fmt.Sprintf("creating response: %s: %s", res.Status, body),
		}
		if readErr != nil {
			failure.diagnostic += fmt.Sprintf(" (reading body: %v)", readErr)
		}
		var envelope struct {
			Error responseError `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			failure.detail = envelope.Error
		}
		return nil, failure, nil
	}
	output, failure, err := decodeResponseStream(res.Body, !auth.fallback)
	if failure != nil {
		failure.headers = res.Header.Clone()
		failure.receivedAt = time.Now()
	}
	return output, failure, err
}

func responseBody(s *Session) ([]byte, error) {
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
	for _, f := range s.Functions {
		tools = append(tools, f.Schema())
	}
	reqObj["tools"] = []map[string]any{
		{
			"type":        "namespace",
			"name":        "default",
			"description": "Default",
			"tools":       tools,
		},
		{
			"type": "web_search",
		},
	}
	startIndex := 0
	for i := len(s.Events) - 1; i >= 0; i-- {
		if t, _ := s.Events[i]["type"].(string); t == "compaction" {
			startIndex = i
			break
		}
	}
	reqObj["input"] = s.Events[startIndex:]
	return json.Marshal(reqObj)
}

package provider

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrQuotaExceeded         = errors.New("API quota exceeded")
	ErrUsageLimitReached     = errors.New("account usage limit reached")
	ErrAuthentication        = errors.New("authentication failed")
	ErrPermissionDenied      = errors.New("permission denied")
	ErrInvalidRequest        = errors.New("invalid request")
	ErrContextLengthExceeded = errors.New("context length exceeded")
	ErrResponseIncomplete    = errors.New("response incomplete")
)

type Function interface {
	Schema() map[string]any
	Call(context.Context, map[string]any) []Part
}

type Session struct {
	ID               string
	Model            string
	Effort           string
	Instructions     string
	CompactThreshold int
	ServiceTier      string
	Functions        []Function
	Events           []map[string]any
}

func NewSession(model, effort, instructions string, functions []Function) *Session {
	return &Session{
		ID:               crand.Text(),
		Model:            model,
		Effort:           effort,
		Instructions:     instructions,
		CompactThreshold: 128000,
		ServiceTier:      "auto",
		Functions:        slices.Clone(functions),
	}
}

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
		turnCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
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

func (c *Client) responses(ctx context.Context, s *Session) ([]map[string]any, error) {
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
	reqBody, err := json.Marshal(reqObj)
	if err != nil {
		return nil, fmt.Errorf("encoding request object: %w", err)
	}
	type responseError struct {
		Code    string            `json:"code"`
		Type    string            `json:"type"`
		Headers map[string]string `json:"headers"`
	}

	delay := 500 * time.Millisecond
	var retryAt time.Time
RETRY:
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	retryAt = time.Time{}
	{
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
		status := res.StatusCode
		headers := res.Header.Clone()
		receivedAt := time.Now()
		var failure responseError
		var diagnostic string
		retryable := status == 408 || status == 409 || status == 429 || status >= 500
		if status != 200 {
			body, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			diagnostic = fmt.Sprintf("creating response: %s: %s", res.Status, body)
			if readErr != nil {
				diagnostic += fmt.Sprintf(" (reading body: %v)", readErr)
			}
			var envelope struct {
				Error responseError `json:"error"`
			}
			if err := json.Unmarshal(body, &envelope); err == nil {
				failure = envelope.Error
			}
			goto FAILED
		}
		{
			mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
			if err != nil || mediaType != "text/event-stream" {
				res.Body.Close()
				return nil, fmt.Errorf("unexpected response Content-Type: %q", res.Header.Get("Content-Type"))
			}
			scanner := bufio.NewScanner(res.Body)
			scanner.Buffer(make([]byte, 4096), 16<<20)
			var data []byte
			for scanner.Scan() {
				line := scanner.Bytes()
				if bytes.HasPrefix(line, []byte("data:")) {
					if len(data)+len(line) > 16<<20 {
						res.Body.Close()
						return nil, errors.New("response event exceeds 16 MiB")
					}
					data = append(data, bytes.TrimPrefix(line[len("data:"):], []byte(" "))...)
					data = append(data, '\n')
					continue
				}
				if len(line) != 0 || len(data) == 0 {
					continue
				}
				var eventType struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal(data, &eventType); err != nil {
					res.Body.Close()
					return nil, fmt.Errorf("decoding response event: %w", err)
				}
				switch eventType.Type {
				case "response.completed", "response.incomplete", "response.failed", "error":
				default:
					data = data[:0]
					continue
				}
				var event struct {
					Type     string         `json:"type"`
					Code     string         `json:"code"`
					Error    *responseError `json:"error"`
					Response *struct {
						ID                string           `json:"id"`
						Status            string           `json:"status"`
						Output            []map[string]any `json:"output"`
						Error             *responseError   `json:"error"`
						IncompleteDetails struct {
							Reason string `json:"reason"`
						} `json:"incomplete_details"`
					} `json:"response"`
				}
				if err := json.Unmarshal(data, &event); err != nil {
					res.Body.Close()
					return nil, fmt.Errorf("decoding response event: %w", err)
				}
				switch event.Type {
				case "response.completed":
					res.Body.Close()
					if event.Response == nil || event.Response.ID == "" || event.Response.Status != "completed" || event.Response.Error != nil || event.Response.Output == nil {
						return nil, errors.New("invalid completed response")
					}
					return event.Response.Output, nil
				case "response.incomplete":
					res.Body.Close()
					if event.Response == nil {
						return nil, fmt.Errorf("%w: missing response", ErrResponseIncomplete)
					}
					return nil, fmt.Errorf("%w: %s", ErrResponseIncomplete, event.Response.IncompleteDetails.Reason)
				case "response.failed", "error":
					failure.Code = event.Code
					if event.Error != nil {
						failure = *event.Error
					}
					if event.Response != nil && event.Response.Error != nil {
						failure = *event.Response.Error
					}
					diagnostic = fmt.Sprintf("response stream failure: %s", data[:min(len(data), 1<<20)])
					status = 0
					receivedAt = time.Now()
					goto FAILED
				}
			}
			res.Body.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("reading response stream: %w", err)
			}
			return nil, fmt.Errorf("response stream ended before completion: %w", io.ErrUnexpectedEOF)
		}
	FAILED:
		res.Body.Close()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var kind error
		switch status {
		case 400, 404, 405, 413, 415, 422, 431:
			kind = ErrInvalidRequest
		case 401:
			kind = ErrAuthentication
		case 403:
			kind = ErrPermissionDenied
		}
		code := failure.Code
	CLASSIFY:
		switch code {
		case "insufficient_quota", "billing_hard_limit_reached", "credit_balance_exhausted",
			"organization_spend_limit_exceeded", "project_spend_limit_exceeded", "organization_usage_limit_exceeded":
			kind = ErrQuotaExceeded
		case "usage_limit_reached", "subscription_sharing_usage_limit_exceeded":
			kind = ErrUsageLimitReached
		case "authentication_error", "invalid_api_key", "invalid_authentication", "invalid_token",
			"token_expired", "subscription_sharing_invalid_user":
			kind = ErrAuthentication
		case "permission_error", "permission_denied", "insufficient_permissions", "policy_error", "usage_not_included",
			"subscription_sharing_user_not_eligible", "subscription_sharing_route_not_supported",
			"chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context",
			"bio_policy", "cyber_policy", "misalignment_policy_violation",
			"image_content_policy_violation", "content_policy_violation":
			kind = ErrPermissionDenied
		case "context_length_exceeded":
			kind = ErrContextLengthExceeded
		case "invalid_request_error", "invalid_prompt", "data_residency_mismatch", "model_not_found",
			"subscription_sharing_unsupported_capability", "invalid_image", "invalid_image_format",
			"invalid_base64_image", "invalid_image_url", "image_too_large", "image_too_small",
			"image_parse_error", "invalid_image_mode", "image_file_too_large", "unsupported_image_media_type",
			"empty_image_file", "image_file_not_found", "audio_too_large", "audio_too_short",
			"invalid_audio", "invalid_audio_data", "invalid_audio_format", "missing_audio_sample_rate":
			kind = ErrInvalidRequest
		case "server_error", "rate_limit_exceeded", "rate_limit_error", "service_unavailable_error", "slow_down",
			"server_is_overloaded", "vector_store_timeout", "policy_capacity_unavailable", "flex_unavailable",
			"subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
			retryable = true
			if failure.Type == "policy_error" {
				break
			}
			fallthrough
		default:
			if code != failure.Type {
				code = failure.Type
				goto CLASSIFY
			}
		}
		if kind != nil {
			return nil, fmt.Errorf("%w: %s", kind, diagnostic)
		}
		for name, value := range failure.Headers {
			headers.Add(name, value)
		}
		for _, value := range headers.Values("X-Should-Retry") {
			if value == "false" {
				return nil, errors.New(diagnostic)
			}
			if value == "true" {
				retryable = true
			}
		}
		for _, value := range headers.Values("Retry-After-Ms") {
			n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if n >= 0 && ((err == nil && !math.IsInf(n, 0)) || errors.Is(err, strconv.ErrRange)) {
				wait := time.Duration(1<<63 - 1)
				nanoseconds := n * float64(time.Millisecond)
				if nanoseconds < float64(wait) {
					wait = time.Duration(nanoseconds)
				}
				if candidate := receivedAt.Add(wait); candidate.After(retryAt) {
					retryAt = candidate
				}
			}
		}
		if retryAt.IsZero() {
			for _, value := range headers.Values("Retry-After") {
				value = strings.TrimSpace(value)
				n, err := strconv.ParseFloat(value, 64)
				if n >= 0 && ((err == nil && !math.IsInf(n, 0)) || errors.Is(err, strconv.ErrRange)) {
					wait := time.Duration(1<<63 - 1)
					nanoseconds := n * float64(time.Second)
					if nanoseconds < float64(wait) {
						wait = time.Duration(nanoseconds)
					}
					if candidate := receivedAt.Add(wait); candidate.After(retryAt) {
						retryAt = candidate
					}
				} else if date, err := http.ParseTime(value); err == nil && date.After(retryAt) {
					retryAt = date
				}
			}
		}
		if !retryable && retryAt.IsZero() {
			return nil, errors.New(diagnostic)
		}
	}
	{
		wait := time.Duration(float64(delay) * (0.75 + rand.Float64()/4))
		wait = max(wait, time.Until(retryAt))
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
	goto RETRY
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

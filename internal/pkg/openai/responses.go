package openai

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

// responses is Next's blocking HTTP/SSE transport. It owns reqObj, forces
// SIWC's transport flags, and decodes the final response object into resObj.
// Progress notifications aren't conversation Events and aren't accumulated.
// Failed, incomplete, malformed, or interrupted streams return errors instead
// of partial output. Requests aren't retried or resumed, since replaying
// inference could duplicate work and plan usage. HTTP errors include the status
// and response body.
//
// See https://developers.openai.com/api/docs/guides/streaming-responses.
func (c *Client) responses(ctx context.Context, reqObj map[string]any, resObj any) error {
	reqObj["stream"] = true
	reqObj["store"] = false
	body, err := json.Marshal(reqObj)
	if err != nil {
		return fmt.Errorf("encoding response request: %w", err)
	}

	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("creating response: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response error body (%s): %w", resp.Status, err)
		}
		return fmt.Errorf("creating response: %s: %s", resp.Status, body)
	}

	// SSE frames end at a blank line. Join their data fields with newlines;
	// event names and comments aren't needed because each JSON payload has type.
	// A Reader avoids Scanner's default line limit for large completed responses.
	reader := bufio.NewReader(resp.Body)
	var data []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("response stream ended before response.completed: %w", io.ErrUnexpectedEOF)
			}
			return fmt.Errorf("reading response stream: %w", err)
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
			return fmt.Errorf("decoding response event: %w", err)
		}
		data = data[:0]
		switch event.Type {
		case "response.completed":
			if err := json.Unmarshal(event.Response, resObj); err != nil {
				return fmt.Errorf("decoding completed response: %w", err)
			}
			return nil
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
				return fmt.Errorf("decoding failed response: %w", err)
			}
			if event.Type == "response.incomplete" {
				return fmt.Errorf("response incomplete: %s", failure.IncompleteDetails.Reason)
			}
			return fmt.Errorf("response failed: %s: %s", failure.Error.Code, failure.Error.Message)
		case "error":
			return fmt.Errorf("response stream error: %s: %s", event.Code, event.Message)
		}
	}
}

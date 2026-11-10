package provider

import (
	"bufio"
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"slices"
)

// decodeResponseStream retains native opaque items but commits them only after a
// valid completion. Signed-in streams may leave the terminal output array empty.
func decodeResponseStream(reader io.Reader, collectItems bool) ([]map[string]any, *responseFailure, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	type completedItem struct {
		item map[string]any
		size int
	}
	var completedItems map[int]completedItem
	completedBytes := 0
	var data []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		if bytes.HasPrefix(line, []byte("data:")) {
			if len(data)+len(line) > 16<<20 {
				return nil, nil, errors.New("response event exceeds 16 MiB")
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
			return nil, nil, fmt.Errorf("decoding response event: %w", err)
		}
		switch eventType.Type {
		case "response.output_item.done":
			if !collectItems {
				data = data[:0]
				continue
			}
		case "response.completed", "response.incomplete", "response.failed", "error":
		default:
			data = data[:0]
			continue
		}
		var event struct {
			Type        string         `json:"type"`
			Code        string         `json:"code"`
			Error       *responseError `json:"error"`
			OutputIndex *int           `json:"output_index"`
			Item        map[string]any `json:"item"`
			Response    *struct {
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
			return nil, nil, fmt.Errorf("decoding response event: %w", err)
		}
		switch event.Type {
		case "response.output_item.done":
			itemType, _ := event.Item["type"].(string)
			if event.OutputIndex == nil || *event.OutputIndex < 0 || itemType == "" {
				return nil, nil, errors.New("invalid completed output item")
			}
			index := *event.OutputIndex
			completedBytes += len(data) - completedItems[index].size
			if completedBytes > 16<<20 {
				return nil, nil, errors.New("completed response output exceeds 16 MiB")
			}
			if completedItems == nil {
				completedItems = make(map[int]completedItem)
			}
			completedItems[index] = completedItem{item: event.Item, size: len(data)}
			data = data[:0]
			continue
		case "response.completed":
			if event.Response == nil || event.Response.ID == "" || event.Response.Status != "completed" || event.Response.Error != nil || event.Response.Output == nil {
				return nil, nil, errors.New("invalid completed response")
			}
			// Signed-in streams can send the full native items only in
			// output_item.done and leave the terminal output array empty.
			// Commit them only after successful completion, in output order,
			// retaining opaque fields needed by later conversation turns.
			if len(event.Response.Output) == 0 && len(completedItems) > 0 {
				indexes := make([]int, 0, len(completedItems))
				for index := range completedItems {
					indexes = append(indexes, index)
				}
				slices.Sort(indexes)
				for _, index := range indexes {
					event.Response.Output = append(event.Response.Output, completedItems[index].item)
				}
			}
			return event.Response.Output, nil, nil
		case "response.incomplete":
			if event.Response == nil {
				return nil, nil, fmt.Errorf("%w: missing response", ErrResponseIncomplete)
			}
			return nil, nil, fmt.Errorf("%w: %s", ErrResponseIncomplete, event.Response.IncompleteDetails.Reason)
		case "response.failed", "error":
			failure := responseError{Code: event.Code}
			if event.Error != nil {
				failure = *event.Error
			}
			if event.Response != nil && event.Response.Error != nil {
				failure = *event.Response.Error
			}
			return nil, &responseFailure{
				detail:     failure,
				diagnostic: fmt.Sprintf("response stream failure: %s", data[:min(len(data), 1<<20)]),
			}, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading response stream: %w", err)
	}
	return nil, nil, fmt.Errorf("response stream ended before completion: %w", io.ErrUnexpectedEOF)
}

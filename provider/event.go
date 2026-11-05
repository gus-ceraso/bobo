package provider

import (
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

type Event struct {
	body map[string]any
}

func (e Event) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.body)
}

func (e *Event) UnmarshalJSON(data []byte) error {
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		return err
	}
	e.body = body
	return nil
}

type Message struct {
	Role    string
	Phase   string
	Content []Part
}

func (e Event) Message() *Message {
	if t, _ := e.body["type"].(string); t != "message" {
		return nil
	}
	var m Message
	m.Role, _ = e.body["role"].(string)
	m.Phase, _ = e.body["phase"].(string)
	m.Content = decodeParts(e.body["content"])
	return &m
}

func (m Message) Event() Event {
	if m.Role != "user" && m.Role != "developer" {
		panic("only user and developer messages are allowed")
	}
	e := Event{body: make(map[string]any)}
	e.body["type"] = "message"
	e.body["role"] = m.Role
	e.body["content"] = encodeParts(m.Content)
	return e
}

type Reasoning struct {
	Summary string
}

func (e Event) Reasoning() *Reasoning {
	if t, _ := e.body["type"].(string); t != "reasoning" {
		return nil
	}
	var parts []string
	summary, _ := e.body["summary"].([]any)
	for _, value := range summary {
		s, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := s["type"].(string); t == "summary_text" {
			text, _ := s["text"].(string)
			parts = append(parts, text)
		}
	}
	return &Reasoning{Summary: strings.Join(parts, "\n")}
}

type Compaction struct{}

func (e Event) Compaction() *Compaction {
	if t, _ := e.body["type"].(string); t != "compaction" {
		return nil
	}
	return &Compaction{}
}

type FunctionCall struct {
	ID        string
	Name      string
	Arguments string
}

func (e Event) FunctionCall() *FunctionCall {
	if t, _ := e.body["type"].(string); t != "function_call" {
		return nil
	}
	var f FunctionCall
	f.ID, _ = e.body["call_id"].(string)
	f.Name, _ = e.body["name"].(string)
	f.Arguments, _ = e.body["arguments"].(string)
	return &f
}

type FunctionCallOutput struct {
	ID     string
	Output []Part
}

func (e Event) FunctionCallOutput() *FunctionCallOutput {
	if t, _ := e.body["type"].(string); t != "function_call_output" {
		return nil
	}
	var f FunctionCallOutput
	f.ID, _ = e.body["call_id"].(string)
	f.Output = decodeParts(e.body["output"])
	return &f
}

func (f FunctionCallOutput) Event() Event {
	e := Event{body: make(map[string]any)}
	e.body["type"] = "function_call_output"
	e.body["call_id"] = f.ID
	e.body["output"] = encodeParts(f.Output)
	return e
}

type ToolCall struct {
	ID    string
	Name  string
	Input string
}

func (e Event) ToolCall() *ToolCall {
	if t, _ := e.body["type"].(string); t != "custom_tool_call" {
		return nil
	}
	var f ToolCall
	f.ID, _ = e.body["call_id"].(string)
	f.Name, _ = e.body["name"].(string)
	f.Input, _ = e.body["input"].(string)
	return &f
}

type ToolCallOutput struct {
	ID     string
	Output []Part
}

func (e Event) ToolCallOutput() *ToolCallOutput {
	if t, _ := e.body["type"].(string); t != "custom_tool_call_output" {
		return nil
	}
	var f ToolCallOutput
	f.ID, _ = e.body["call_id"].(string)
	f.Output = decodeParts(e.body["output"])
	return &f
}

func (f ToolCallOutput) Event() Event {
	e := Event{body: make(map[string]any)}
	e.body["type"] = "custom_tool_call_output"
	e.body["call_id"] = f.ID
	e.body["output"] = encodeParts(f.Output)
	return e
}

type Part struct {
	ContentType string
	Content     []byte
}

func encodeParts(parts []Part) []any {
	content := make([]any, 0, len(parts))
	for _, p := range parts {
		contentType, _, err := mime.ParseMediaType(p.ContentType)
		if err != nil {
			p.ContentType = http.DetectContentType(p.Content)
			contentType, _, _ = mime.ParseMediaType(p.ContentType)
		}
		if contentType == "text/plain" {
			content = append(content, map[string]any{
				"type": "input_text",
				"text": string(p.Content),
			})
			continue
		}
		data := "data:" + p.ContentType + ";base64," + base64.StdEncoding.EncodeToString(p.Content)
		if strings.HasPrefix(contentType, "image/") {
			content = append(content, map[string]any{
				"type":      "input_image",
				"image_url": data,
			})
			continue
		}
		content = append(content, map[string]any{
			"type":      "input_file",
			"file_data": data,
		})
	}
	return content
}

func decodeParts(content any) []Part {
	if text, ok := content.(string); ok {
		return []Part{{ContentType: "text/plain", Content: []byte(text)}}
	}
	items, _ := content.([]any)
	var parts []Part
	for _, value := range items {
		c, ok := value.(map[string]any)
		if !ok {
			continue
		}
		t, _ := c["type"].(string)
		switch t {
		case "input_text", "output_text", "refusal":
			key := "text"
			if t == "refusal" {
				key = "refusal"
			}
			text, _ := c[key].(string)
			parts = append(parts, Part{ContentType: "text/plain", Content: []byte(text)})
		case "input_image", "input_file":
			key := "image_url"
			if t == "input_file" {
				key = "file_data"
			}
			data, ok := c[key].(string)
			if !ok {
				continue
			}
			var p Part
			var err error
			if strings.HasPrefix(data, "data:") {
				header, body, ok := strings.Cut(data[len("data:"):], ",")
				if !ok {
					continue
				}
				p.ContentType = strings.TrimSuffix(header, ";base64")
				if strings.HasSuffix(header, ";base64") {
					p.Content, err = base64.StdEncoding.DecodeString(body)
				} else {
					var text string
					text, err = url.PathUnescape(body)
					p.Content = []byte(text)
				}
			} else if t == "input_file" {
				p.Content, err = base64.StdEncoding.DecodeString(data)
			} else {
				continue
			}
			if err == nil {
				if _, _, err := mime.ParseMediaType(p.ContentType); err != nil {
					p.ContentType = http.DetectContentType(p.Content)
				}
				parts = append(parts, p)
			}
		}
	}
	return parts
}

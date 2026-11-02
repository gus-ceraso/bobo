package provider

import (
	"encoding/json"
	"strings"
)

type Event struct {
	body map[string]any
}

func (e Event) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.body)
}

func (e *Event) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &e.body)
}

type Message struct {
	Role   string
	Phase  string
	Text   string
	Images []string
}

func (e Event) Message() *Message {
	if t, _ := e.body["type"].(string); t != "message" {
		return nil
	}
	var m Message
	m.Role, _ = e.body["role"].(string)
	m.Phase, _ = e.body["phase"].(string)
	if content, ok := e.body["content"].(string); ok {
		m.Text = content
	} else if content, ok := e.body["content"].([]any); ok {
		var parts []string
		for _, value := range content {
			c, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := c["type"].(string); t == "input_text" || t == "output_text" {
				v, _ := c["text"].(string)
				parts = append(parts, v)
			} else if t == "refusal" {
				v, _ := c["refusal"].(string)
				parts = append(parts, v)
			} else if t == "input_image" {
				v, _ := c["image_url"].(string)
				m.Images = append(m.Images, v)
			}
		}
		m.Text = strings.Join(parts, "\n")
	}
	return &m
}

func (m Message) Event() Event {
	if m.Role != "user" && m.Role != "developer" {
		panic("only user and developer messages are allowed")
	}
	e := Event{body: make(map[string]any)}
	e.body["type"] = "message"
	e.body["role"] = m.Role
	var content []any
	if m.Text != "" {
		content = append(content, map[string]any{
			"type": "input_text",
			"text": m.Text,
		})
	}
	for _, img := range m.Images {
		content = append(content, map[string]any{
			"type":      "input_image",
			"image_url": img,
		})
	}
	e.body["content"] = content
	return e
}

type Reasoning struct {
	Summary string
}

func (e Event) Reasoning() *Reasoning {
	if t, _ := e.body["type"].(string); t != "reasoning" {
		return nil
	}
	p, _ := e.body["summary"].([]any)
	var parts []string
	for _, value := range p {
		s, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := s["type"]; t == "summary_text" {
			v, _ := s["text"].(string)
			parts = append(parts, v)
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
	Arguments map[string]any
}

func (e Event) FunctionCall() *FunctionCall {
	if t, _ := e.body["type"].(string); t != "function_call" {
		return nil
	}
	var f FunctionCall
	f.ID, _ = e.body["call_id"].(string)
	f.Name, _ = e.body["name"].(string)
	args, _ := e.body["arguments"].(string)
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber()
	_ = dec.Decode(&f.Arguments)
	return &f
}

type FunctionCallOutput struct {
	ID     string
	Text   string
	Images []string
}

func (f FunctionCallOutput) Event() Event {
	e := Event{body: make(map[string]any)}
	e.body["type"] = "function_call_output"
	e.body["call_id"] = f.ID
	if len(f.Images) == 0 {
		e.body["output"] = f.Text
		return e
	}
	var content []any
	if f.Text != "" {
		content = append(content, map[string]any{
			"type": "input_text",
			"text": f.Text,
		})
	}
	for _, img := range f.Images {
		content = append(content, map[string]any{
			"type":      "input_image",
			"image_url": img,
		})
	}
	e.body["output"] = content
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
	Text   string
	Images []string
}

func (f ToolCallOutput) Event() Event {
	e := Event{body: make(map[string]any)}
	e.body["type"] = "custom_tool_call_output"
	e.body["call_id"] = f.ID
	if len(f.Images) == 0 {
		e.body["output"] = f.Text
		return e
	}
	var content []any
	if f.Text != "" {
		content = append(content, map[string]any{
			"type": "input_text",
			"text": f.Text,
		})
	}
	for _, img := range f.Images {
		content = append(content, map[string]any{
			"type":      "input_image",
			"image_url": img,
		})
	}
	e.body["output"] = content
	return e
}

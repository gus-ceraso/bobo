package openrouter

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Tool interface {
	Schema() map[string]any
	Call(map[string]any) any
}

type Message struct {
	Role      string `json:"role"`
	Content   any    `json:"content"`
	ToolCalls []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Tool struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningDetails any    `json:"reasoning_details,omitempty"`
}

type Session struct {
	Model    string
	Effort   string
	Cache    bool
	Limit    int
	Tools    []Tool
	Messages []Message
}

func (c *Client) NewSession(model, effort, systemPrompt string, cache bool, tools []Tool) (*Session, error) {
	if model == "" {
		return nil, fmt.Errorf("invalid arguments: empty model")
	}
	var resObj struct {
		Data struct {
			Architecture struct {
				InputModalities []string `json:"input_modalities"`
			} `json:"architecture"`
			ContextLength int `json:"context_length"`
			Reasoning     struct {
				SupportedEfforts []string `json:"supported_efforts"`
			} `json:"reasoning"`
		} `json:"data"`
	}
	if err := c.do("GET", "/model/"+model, nil, &resObj); err != nil {
		return nil, fmt.Errorf("fetching model information: %s", err)
	}
	if effort != "" {
		effortSupported := false
		for _, se := range resObj.Data.Reasoning.SupportedEfforts {
			if se == effort {
				effortSupported = true
				break
			}
		}
		if !effortSupported {
			return nil, fmt.Errorf("invalid arguments: effort not supported by model")
		}
	}
	images := false
	for _, im := range resObj.Data.Architecture.InputModalities {
		if im == "image" {
			images = true
		}
	}
	if !images {
		return nil, fmt.Errorf("invalid arguments: images not supported by model")
	}
	var s Session
	s.Model = model
	s.Effort = effort
	s.Limit = (3*resObj.Data.ContextLength + 1) / 4
	s.Cache = cache
	s.Tools = make([]Tool, len(tools))
	copy(s.Tools, tools)
	c.addMessage(&s, &Message{Role: "system", Content: systemPrompt})
	return &s, nil
}

func (c *Client) Prompt(s *Session, prompt string) (string, error) {
	c.addMessage(s, &Message{Role: "user", Content: prompt})
	var reqObj struct {
		Model        string `json:"model,omitempty"`
		Effort       string `json:"reasoning_effort,omitempty"`
		CacheControl *struct {
			Type string `json:"type"`
			TTL  string `json:"ttl"`
		} `json:"cache_control,omitempty"`
		Messages []Message `json:"messages"`
		Tools    []struct {
			Type   string         `json:"type"`
			Schema map[string]any `json:"function"`
		} `json:"tools"`
		AllowParallelCalls bool `json:"parallel_tool_calls"`
	}
	reqObj.Model = s.Model
	reqObj.Effort = s.Effort
	if s.Cache {
		reqObj.CacheControl = &struct {
			Type string `json:"type"`
			TTL  string `json:"ttl"`
		}{"ephemeral", "5m"}
	}
	reqObj.Tools = make([]struct {
		Type   string         `json:"type"`
		Schema map[string]any `json:"function"`
	}, len(s.Tools))
	for i := 0; i < len(s.Tools); i++ {
		reqObj.Tools[i].Type = "function"
		reqObj.Tools[i].Schema = s.Tools[i].Schema()
	}
	reqObj.AllowParallelCalls = true
	for {
		reqObj.Messages = s.Messages
		var resObj struct {
			Choices []struct {
				FinishReason string  `json:"finish_reason"`
				Message      Message `json:"message"`
			} `json:"choices"`
			Usage struct {
				Total int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := c.do("POST", "/chat/completions", &reqObj, &resObj); err != nil {
			return "", fmt.Errorf("sending prompt: %s", err)
		}
		if len(resObj.Choices) == 0 {
			return "", fmt.Errorf("no choices")
		}
		m := resObj.Choices[0].Message
		c.addMessage(s, &m)
		if r := resObj.Choices[0].FinishReason; r != "" && r != "tool_calls" && r != "stop" {
			return "", fmt.Errorf("finish reason: %s", r)
		}
		if len(m.ToolCalls) == 0 {
			switch content := m.Content.(type) {
			case string:
				return content, nil
			case map[string]any:
				if maybeText, ok := content["text"]; ok {
					if text, ok := maybeText.(string); ok {
						return text, nil
					}
				}
			}
			return "", nil
		}
		for _, call := range m.ToolCalls {
			found := false
			for _, tool := range s.Tools {
				schema := tool.Schema()
				if schema["name"].(string) == call.Tool.Name {
					found = true
					var args map[string]any
					dec := json.NewDecoder(strings.NewReader(call.Tool.Arguments))
					dec.UseNumber()
					if err := dec.Decode(&args); err != nil {
						c.addMessage(s, &Message{
							Role:       "tool",
							Content:    "{\"error\": \"invalid arguments: JSON decoding arguments\"}",
							ToolCallID: call.ID,
						})
					} else {
						c.addMessage(s, &Message{
							Role:       "tool",
							Content:    tool.Call(args),
							ToolCallID: call.ID,
						})
					}
					break
				}
			}
			if !found {
				c.addMessage(s, &Message{
					Role:       "tool",
					Content:    "{\"error\": \"tool not found\"}",
					ToolCallID: call.ID,
				})
				continue
			}
		}
		if resObj.Usage.Total >= s.Limit {
			c.addMessage(s, &Message{
				Role:    "developer",
				Content: "Context is nearly full. DO NOT call more tools. Give the user a handoff prompt summarizing the task, progress, and next steps.",
			})
		}
	}
}

func (c *Client) addMessage(s *Session, e *Message) {
	s.Messages = append(s.Messages, *e)
	if c.Log == nil {
		return
	}
	if j, err := json.Marshal(e); err == nil {
		c.Log.Println(string(j))
	}
}

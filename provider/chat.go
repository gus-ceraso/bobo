package openrouter

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Procedure interface {
	Schema() map[string]any
	Call(map[string]any) any
}

type Event struct {
	Type    string `json:"role"`
	Content any    `json:"content"`
	Calls   []struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Procedure struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
	CallID           string `json:"tool_call_id,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningDetails any    `json:"reasoning_details,omitempty"`
}

type Session struct {
	Model      string
	Effort     string
	Cache      bool
	Limit      int
	Procedures []Procedure
	Events     []Event
}

func (c *Client) NewSession(model, effort, systemPrompt string, cache bool, procedures []Procedure) (*Session, error) {
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
	s.Procedures = make([]Procedure, len(procedures))
	copy(s.Procedures, procedures)
	s.Events = make([]Event, 1)
	s.Events[0] = Event{Type: "system", Content: systemPrompt}
	return &s, nil
}

func (c *Client) Send(s *Session, prompt string) (string, error) {
	s.Events = append(s.Events, Event{Type: "user", Content: prompt})
	var reqObj struct {
		Model        string `json:"model,omitempty"`
		Effort       string `json:"reasoning_effort,omitempty"`
		CacheControl *struct {
			Type string `json:"type"`
			TTL  string `json:"ttl"`
		} `json:"cache_control,omitempty"`
		Events []Event `json:"messages"`
		Tools  []struct {
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
	}, len(s.Procedures))
	for i := 0; i < len(s.Procedures); i++ {
		reqObj.Tools[i].Type = "function"
		reqObj.Tools[i].Schema = s.Procedures[i].Schema()
	}
	reqObj.AllowParallelCalls = true
	for {
		reqObj.Events = s.Events
		var resObj struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Event        Event  `json:"message"`
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
		if r := resObj.Choices[0].FinishReason; r != "" && r != "tool_calls" && r != "stop" {
			return "", fmt.Errorf("finish reason: %s", r)
		}
		e := resObj.Choices[0].Event
		s.Events = append(s.Events, e)
		if len(e.Calls) == 0 {
			switch content := e.Content.(type) {
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
		for _, call := range e.Calls {
			found := false
			for _, procedure := range s.Procedures {
				schema := procedure.Schema()
				if schema["name"].(string) == call.Procedure.Name {
					found = true
					var args map[string]any
					dec := json.NewDecoder(strings.NewReader(call.Procedure.Arguments))
					dec.UseNumber()
					if err := dec.Decode(&args); err != nil {
						s.Events = append(s.Events, Event{
							Type:    "tool",
							Content: "{\"error\": \"invalid arguments: JSON decoding arguments\"}",
							CallID:  call.ID,
						})
					} else {
						s.Events = append(s.Events, Event{
							Type:    "tool",
							Content: procedure.Call(args),
							CallID:  call.ID,
						})
					}
					break
				}
			}
			if !found {
				s.Events = append(s.Events, Event{
					Type:    "tool",
					Content: "{\"error\": \"procedure not found\"}",
					CallID:  call.ID,
				})
				continue
			}
		}
		if resObj.Usage.Total >= s.Limit {
			s.Events = append(s.Events, Event{
				Type:    "developer",
				Content: "Context is nearly full. DO NOT call more tools. Give the user a handoff prompt summarizing the task, progress, and next steps.",
			})
		}
	}
}

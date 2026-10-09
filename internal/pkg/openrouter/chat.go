package openrouter

import (
	"encoding/json"
	"fmt"
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
	} `json:"tool_calls"`
	CallID           string `json:"tool_call_id"`
	Reasoning        string `json:"reasoning"`
	ReasoningDetails any    `json:"reasoning_details"`
}

type Session struct {
	Model      string
	Effort     string
	Limit      int
	Procedures []Procedure
	Events     []Event
}

func (c *Client) NewSession(model, effort, systemPrompt string, procedures []Procedure) (*Session, error) {
	if model == "" {
		return nil, fmt.Errorf("invalid arguments: empty model")
	}
	var resObj struct {
		Data struct {
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
	var s Session
	s.Model = model
	s.Effort = effort
	s.Limit = (3*resObj.Data.ContextLength + 1) / 4
	s.Procedures = make([]Procedure, len(procedures))
	copy(s.Procedures, procedures)
	s.Events = make([]Event, 1)
	s.Events[0] = Event{Type: "system", Content: systemPrompt}
	return &s, nil
}

func (c *Client) Send(s *Session, prompt string) (reply string, err error) {
	s.Events = append(s.Events, Event{Type: "user", Content: prompt})
	var reqObj struct {
		Model  string  `json:"model"`
		Effort string  `json:"reasoning_effort"`
		Events []Event `json:"messages"`
		Tools  []struct {
			Type   string         `json:"type"`
			Schema map[string]any `json:"function"`
		} `json:"tools"`
		AllowParallelCalls bool `json:"parallel_tool_calls"`
	}
	reqObj.Model = s.Model
	reqObj.Effort = s.Effort
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
			return reply, fmt.Errorf("sending prompt: %s", err)
		}
		if len(resObj.Choices) == 0 {
			return
		}
		if r := resObj.Choices[0].FinishReason; r != "" && r != "tool_calls" && r != "stop" {
			return reply, fmt.Errorf("finish reason: %s", r)
		}
		e := resObj.Choices[0].Event
		s.Events = append(s.Events, e)
		switch content := e.Content.(type) {
		case string:
			reply += content
		case map[string]any:
			if maybeText, ok := content["text"]; ok {
				if text, ok := maybeText.(string); ok {
					reply += text
				}
			}
		}
		if len(e.Calls) == 0 {
			return
		}
		for _, call := range e.Calls {
			for _, procedure := range s.Procedures {
				schema := procedure.Schema()
				if schema["name"] == call.Procedure.Name {
					var args map[string]any
					var retv string
					if err := json.Unmarshal([]byte(call.Procedure.Arguments), &args); err != nil {
						retv = "invalid arguments: expected valid JSON for " + call.Procedure.Name
					} else {
						ret, err := json.Marshal(procedure.Call(args))
						if err != nil {
							return reply, fmt.Errorf("JSON encoding %s return: %s", schema["name"], err)
						}
						retv = string(ret)
					}
					s.Events = append(s.Events, Event{
						Type:    "tool",
						Content: retv,
						CallID:  call.ID,
					})
					break
				}
			}
		}
		if s.Limit-resObj.Usage.Total <= 1 {
			s.Events = append(s.Events, Event{
				Type:    "developer",
				Content: "Context is nearly full. DO NOT call more tools. Give the user a handoff prompt summarizing the task, progress, and next steps.",
			})
		}
	}
}

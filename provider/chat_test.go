package openrouter

import (
	"os"
	"strings"
	"testing"
)

type GetWeather struct{}

func (_ GetWeather) Schema() map[string]any {
	return map[string]any{
		"name":        "get_weather",
		"description": "Get current weather",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{
					"type": "string",
				},
			},
			"required": []string{"location"},
		},
	}
}

func (_ GetWeather) Call(args map[string]any) any {
	locationArg, ok := args["location"]
	if !ok {
		return "invalid arguments: missing location"
	}
	location, ok := locationArg.(string)
	if !ok {
		return "invalid arguments: location is not string"
	}
	switch {
	case strings.Contains(location, "São Paulo"):
		return map[string]any{
			"temperature": 23,
			"weather":     "Clear",
		}
	case strings.Contains(location, "London"):
		return map[string]any{
			"temperature": 11,
			"weather":     "Light Rain",
		}
	case strings.Contains(location, "Tokyo"):
		return map[string]any{
			"temperature": 21,
			"weather":     "Sunny",
		}
	}
	return "No data available"
}

func TestEventLoop(t *testing.T) {
	token := os.Getenv("OPENROUTER_API_KEY")
	if token == "" {
		t.Skip("skipping openrouter event loop test because OPENROUTER_API_KEY is missing")
	}
	c := Client{Token: token}
	s, err := c.NewSession("openai/gpt-6-luna", "low", "You are a helpful assistant.", false, []Procedure{GetWeather{}})
	if err != nil {
		t.Fatalf("failed new session: %s", err)
	}
	p := "What's the weather like in São Paulo?"
	t.Log(p)
	r, err := c.Send(s, p)
	if err != nil {
		t.Fatalf("failed send: %s", err)
	}
	t.Log(r)
	p = "What about London? Tokyo?"
	t.Log(p)
	r, err = c.Send(s, p)
	if err != nil {
		t.Fatalf("failed send: %s", err)
	}
	t.Log(r)
	p = "Chicago?"
	t.Log(p)
	r, err = c.Send(s, p)
	if err != nil {
		t.Fatalf("failed send: %s", err)
	}
	t.Log(r)
}

package procedure

import (
	"fmt"

	web "ceraso.dev/bobo/web"
)

type Search struct {
	Client *web.Client
}

func (_ *Search) Schema() map[string]any {
	return map[string]any{
		"name":        "search",
		"description": "Search the web using a natural text query via Exa.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Natural text query. E.g. \"Who won the 2026 World Cup?\"",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (t *Search) Call(args map[string]any) any {
	var metadata struct {
		Error string `json:"error,omitempty"`
		Query string `json:"query,omitempty"`
		Total int    `json:"total,omitempty"`
	}

	metadata.Query = argString(args, "query")
	if metadata.Query == "" {
		metadata.Error = "invalid arguments: missing query string"
		return enc(metadata)
	}

	results, err := t.Client.Search(metadata.Query)
	if err != nil {
		metadata.Error = fmt.Sprintf("exa search: %s", err)
		return enc(metadata)
	}
	metadata.Total = len(results)
	if metadata.Total == 0 {
		return enc(metadata)
	}
	return []map[string]any{
		map[string]any{
			"type": "text",
			"text": enc(metadata),
		},
		map[string]any{
			"type": "text",
			"text": enc(results),
		},
	}
}

package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Model describes a model offered for display in the signed-in account's catalog.
type Model struct {
	Slug string `json:"slug"`         // Identifier to send as the request's model.
	Name string `json:"display_name"` // Human-readable name for a model picker.
}

// Models returns the account's models intended for display, preserving server
// order. It excludes entries whose visibility isn't "list". Use Slug, not
// Name, for inference.
//
// Models refreshes expired credentials before requesting the catalog. It doesn't
// cache the catalog or retry failed requests. ctx controls both renewal and the
// catalog request.
//
// See https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("reading models error response (%s): %w", resp.Status, err)
		}
		return nil, fmt.Errorf("listing models: %s: %s", resp.Status, body)
	}

	// The SIWC catalog uses "models", not the API-key catalog's "data" array.
	var catalog struct {
		Models []struct {
			Model
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decoding models: %w", err)
	}
	models := make([]Model, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		if model.Visibility == "list" {
			models = append(models, model.Model)
		}
	}
	return models, nil
}

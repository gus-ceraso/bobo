package provider

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"mime"
	"net/http"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"
)

func TestResponsesErrorCodeAliases(t *testing.T) {
	for _, group := range []struct {
		want  error
		codes []string
	}{
		{ErrQuotaExceeded, []string{"insufficient_quota", "billing_hard_limit_reached", "credit_balance_exhausted", "organization_spend_limit_exceeded", "project_spend_limit_exceeded", "organization_usage_limit_exceeded"}},
		{ErrUsageLimitReached, []string{"usage_limit_reached", "subscription_sharing_usage_limit_exceeded"}},
		{ErrAuthentication, []string{"authentication_error", "invalid_api_key", "invalid_authentication", "invalid_token", "token_expired", "subscription_sharing_invalid_user"}},
		{ErrPermissionDenied, []string{"permission_error", "permission_denied", "insufficient_permissions", "policy_error", "usage_not_included", "subscription_sharing_user_not_eligible", "subscription_sharing_route_not_supported", "chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context", "bio_policy", "cyber_policy", "misalignment_policy_violation", "image_content_policy_violation", "content_policy_violation"}},
		{ErrContextLengthExceeded, []string{"context_length_exceeded"}},
		{ErrInvalidRequest, []string{"invalid_request_error", "invalid_prompt", "data_residency_mismatch", "model_not_found", "subscription_sharing_unsupported_capability", "invalid_image", "invalid_image_format", "invalid_base64_image", "invalid_image_url", "image_too_large", "image_too_small", "image_parse_error", "invalid_image_mode", "image_file_too_large", "unsupported_image_media_type", "empty_image_file", "image_file_not_found", "audio_too_large", "audio_too_short", "invalid_audio", "invalid_audio_data", "invalid_audio_format", "missing_audio_sample_rate"}},
	} {
		for _, code := range group.codes {
			t.Run(code, func(t *testing.T) {
				c := mockClient(func(*http.Request) (*http.Response, error) {
					return rawResponse(429, "application/json", `{"error":{"code":"`+code+`"}}`), nil
				})
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if _, err := c.responses(ctx, NewSession("model", "low", "", nil)); !errors.Is(err, group.want) {
					t.Fatalf("code %s = %v, want %v", code, err, group.want)
				}
			})
		}
	}
}

func TestResponsesTransientCodeAliases(t *testing.T) {
	for _, code := range []string{"server_error", "rate_limit_exceeded", "rate_limit_error", "service_unavailable_error", "slow_down", "server_is_overloaded", "vector_store_timeout", "policy_capacity_unavailable", "flex_unavailable", "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable"} {
		t.Run(code, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				c := mockClient(func(*http.Request) (*http.Response, error) {
					attempts++
					if attempts == 1 {
						return streamResponse(`{"type":"error","error":{"code":"` + code + `"}}`), nil
					}
					return completedResponse([]map[string]any{}), nil
				})
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if _, err := c.responses(ctx, NewSession("model", "low", "", nil)); err != nil || attempts != 2 {
					t.Fatalf("transient code %s = %v, attempts=%d", code, err, attempts)
				}
			})
		})
	}
}

func TestResponsesLatestCompaction(t *testing.T) {
	s := NewSession("model", "low", "", nil)
	s.Events = []map[string]any{
		{"type": "message", "content": "old"},
		{"type": "compaction", "encrypted_content": "first"},
		{"type": "message", "content": "intermediate"},
		{"type": "compaction", "encrypted_content": "second"},
		{"type": "message", "content": "current"},
	}
	c := mockClient(func(req *http.Request) (*http.Response, error) {
		var body struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.UnmarshalRead(req.Body, &body); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(body.Input, s.Events[3:]) {
			t.Fatalf("request did not use latest compaction: %#v", body.Input)
		}
		return completedResponse([]map[string]any{}), nil
	})
	if _, err := c.responses(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(s.Events) != 5 {
		t.Fatal("request construction mutated session history")
	}
}

func FuzzPartsRoundTrip(f *testing.F) {
	for _, seed := range []struct {
		kind uint8
		data []byte
	}{
		{0, []byte("hello 世界🙂\n\x00")}, {0, nil}, {1, []byte{0, 1, 2, 255}},
		{2, []byte("PDF-like bytes")}, {3, []byte{255, 0, 128}}, {4, []byte("unknown media type")},
	} {
		f.Add(seed.kind, seed.data)
	}
	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		contentType := []string{"text/plain", "image/png", "application/pdf", "application/octet-stream", "invalid;"}[int(kind)%5]
		if contentType == "text/plain" && !utf8.Valid(data) {
			t.Skip()
		}
		encoded := encodeParts([]Part{{ContentType: contentType, Content: data}})
		got := decodeParts(encoded)
		if len(got) != 1 || !bytes.Equal(got[0].Content, data) {
			t.Fatalf("round trip changed payload: %#v", got)
		}
		if _, _, err := mime.ParseMediaType(got[0].ContentType); err != nil {
			t.Fatalf("round trip produced invalid media type: %q", got[0].ContentType)
		}
		if second := decodeParts(encodeParts(got)); len(second) != 1 || !bytes.Equal(second[0].Content, data) {
			t.Fatal("second round trip changed payload")
		}
	})
}

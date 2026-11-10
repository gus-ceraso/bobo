package provider

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func classifyResponseFailure(status int, failure responseError) (kind error, retryable bool) {
	retryable = status == 408 || status == 409 || status == 429 || status >= 500
	switch status {
	case 400, 404, 405, 413, 415, 422, 431:
		kind = ErrInvalidRequest
	case 401:
		kind = ErrAuthentication
	case 403:
		kind = ErrPermissionDenied
	}
	for code := failure.Code; ; code = failure.Type {
		classified, transient := classifyResponseCode(code)
		if classified != nil {
			return classified, retryable
		}
		retryable = retryable || transient
		if code == failure.Type || transient && failure.Type == "policy_error" {
			return kind, retryable
		}
	}
}

func classifyResponseCode(code string) (error, bool) {
	switch code {
	case "insufficient_quota", "billing_hard_limit_reached", "credit_balance_exhausted",
		"organization_spend_limit_exceeded", "project_spend_limit_exceeded", "organization_usage_limit_exceeded":
		return ErrQuotaExceeded, false
	case "usage_limit_reached", "subscription_sharing_usage_limit_exceeded":
		return ErrUsageLimitReached, false
	case "authentication_error", "invalid_api_key", "invalid_authentication", "invalid_token",
		"token_expired", "subscription_sharing_invalid_user":
		return ErrAuthentication, false
	case "permission_error", "permission_denied", "insufficient_permissions", "policy_error", "usage_not_included",
		"subscription_sharing_user_not_eligible", "subscription_sharing_route_not_supported",
		"chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context",
		"bio_policy", "cyber_policy", "misalignment_policy_violation",
		"image_content_policy_violation", "content_policy_violation":
		return ErrPermissionDenied, false
	case "context_length_exceeded":
		return ErrContextLengthExceeded, false
	case "invalid_request_error", "invalid_prompt", "data_residency_mismatch", "model_not_found",
		"subscription_sharing_unsupported_capability", "invalid_image", "invalid_image_format",
		"invalid_base64_image", "invalid_image_url", "image_too_large", "image_too_small",
		"image_parse_error", "invalid_image_mode", "image_file_too_large", "unsupported_image_media_type",
		"empty_image_file", "image_file_not_found", "audio_too_large", "audio_too_short",
		"invalid_audio", "invalid_audio_data", "invalid_audio_format", "missing_audio_sample_rate":
		return ErrInvalidRequest, false
	case "server_error", "rate_limit_exceeded", "rate_limit_error", "service_unavailable_error", "slow_down",
		"server_is_overloaded", "vector_store_timeout", "policy_capacity_unavailable", "flex_unavailable",
		"subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
		return nil, true
	}
	return nil, false
}

// responseRetry merges HTTP and stream hints. An explicit false wins, and
// Retry-After-Ms takes precedence over Retry-After; repeated values use the latest.
func responseRetry(failure *responseFailure, retryable bool) (retryAt time.Time, retry bool) {
	headers := failure.headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	for name, value := range failure.detail.Headers {
		headers.Add(name, value)
	}
	for _, value := range headers.Values("X-Should-Retry") {
		if value == "false" {
			return time.Time{}, false
		}
		if value == "true" {
			retryable = true
		}
	}
	for _, value := range headers.Values("Retry-After-Ms") {
		if wait, ok := retryDelay(value, time.Millisecond); ok {
			if candidate := failure.receivedAt.Add(wait); candidate.After(retryAt) {
				retryAt = candidate
			}
		}
	}
	if retryAt.IsZero() {
		for _, value := range headers.Values("Retry-After") {
			var candidate time.Time
			if wait, ok := retryDelay(value, time.Second); ok {
				candidate = failure.receivedAt.Add(wait)
			} else {
				candidate, _ = http.ParseTime(strings.TrimSpace(value))
			}
			if candidate.After(retryAt) {
				retryAt = candidate
			}
		}
	}
	return retryAt, retryable || !retryAt.IsZero()
}

// retryDelay accepts nonnegative finite seconds/milliseconds and saturates
// numeric overflow rather than wrapping a large server delay into an early retry.
func retryDelay(value string, unit time.Duration) (time.Duration, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if !(n >= 0 && ((err == nil && !math.IsInf(n, 0)) || errors.Is(err, strconv.ErrRange))) {
		return 0, false
	}
	wait := time.Duration(1<<63 - 1)
	if nanoseconds := n * float64(unit); nanoseconds < float64(wait) {
		wait = time.Duration(nanoseconds)
	}
	return wait, true
}

func waitResponseRetry(ctx context.Context, delay time.Duration, retryAt time.Time) error {
	wait := time.Duration(float64(delay) * (0.75 + rand.Float64()/4))
	timer := time.NewTimer(max(wait, time.Until(retryAt)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

package provider

import (
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestClassifyResponseFailure(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		code, typ string
		kind      error
		retryable bool
	}{
		{"unknown", 418, "unknown", "", nil, false},
		{"HTTP retry", 503, "", "", nil, true},
		{"HTTP authentication", 401, "", "", ErrAuthentication, false},
		{"code overrides HTTP", 401, "insufficient_quota", "", ErrQuotaExceeded, false},
		{"code overrides type", 429, "context_length_exceeded", "server_error", ErrContextLengthExceeded, true},
		{"type fallback", 200, "unknown", "invalid_request_error", ErrInvalidRequest, false},
		{"transient policy capacity", 200, "server_error", "policy_error", nil, true},
		{"transient with terminal type", 200, "server_error", "authentication_error", ErrAuthentication, true},
		{"transient cannot override HTTP permission", 403, "server_error", "", ErrPermissionDenied, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			kind, retryable := classifyResponseFailure(tt.status, responseError{Code: tt.code, Type: tt.typ})
			if kind != tt.kind || retryable != tt.retryable {
				t.Fatalf("classification = (%v, %t), want (%v, %t)", kind, retryable, tt.kind, tt.retryable)
			}
		})
	}
}

func TestResponseRetryHints(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name      string
		headers   http.Header
		stream    map[string]string
		retryable bool
		wait      time.Duration
		retry     bool
	}{
		{name: "no hint"},
		{name: "transient", retryable: true, retry: true},
		{name: "explicit retry", stream: map[string]string{"x-should-retry": "true"}, retry: true},
		{name: "false wins", headers: http.Header{"X-Should-Retry": {"true", "false"}, "Retry-After": {"10"}}, retryable: true},
		{name: "stream false wins", headers: http.Header{"X-Should-Retry": {"true"}}, stream: map[string]string{"x-should-retry": "false"}},
		{name: "latest milliseconds", headers: http.Header{"Retry-After-Ms": {"20", "80", "50"}, "Retry-After": {"10"}}, wait: 80 * time.Millisecond, retry: true},
		{name: "merge stream hint", headers: http.Header{"Retry-After-Ms": {"20"}}, stream: map[string]string{"retry-after-ms": "80"}, wait: 80 * time.Millisecond, retry: true},
		{name: "invalid milliseconds", headers: http.Header{"Retry-After-Ms": {"NaN", "-1"}, "Retry-After": {"0.5"}}, wait: 500 * time.Millisecond, retry: true},
		{name: "HTTP date", headers: http.Header{"Retry-After": {now.Add(time.Minute).Format(http.TimeFormat)}}, wait: time.Minute, retry: true},
		{name: "zero still enables retry", headers: http.Header{"Retry-After-Ms": {"0"}}, retry: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.headers.Clone()
			failure := &responseFailure{headers: tt.headers, receivedAt: now, detail: responseError{Headers: tt.stream}}
			at, retry := responseRetry(failure, tt.retryable)
			want := time.Time{}
			if tt.wait != 0 || tt.name == "zero still enables retry" {
				want = now.Add(tt.wait)
			}
			if retry != tt.retry || !at.Equal(want) {
				t.Fatalf("retry = (%v, %t), want (%v, %t)", at, retry, want, tt.retry)
			}
			if !reflect.DeepEqual(before, tt.headers) {
				t.Fatal("retry processing mutated the HTTP headers")
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	for _, tt := range []struct {
		value string
		unit  time.Duration
		want  time.Duration
		valid bool
	}{
		{" 1.5 ", time.Second, 1500 * time.Millisecond, true},
		{"1.5", time.Millisecond, 1500 * time.Microsecond, true},
		{"-0", time.Second, 0, true},
		{"1e-9999", time.Second, 0, true},
		{"1e9999", time.Second, time.Duration(1<<63 - 1), true},
		{"9223372036854775807", time.Second, time.Duration(1<<63 - 1), true},
		{"-1", time.Second, 0, false},
		{"-1e9999", time.Second, 0, false},
		{"Inf", time.Second, 0, false},
		{"NaN", time.Second, 0, false},
		{"", time.Second, 0, false},
	} {
		got, valid := retryDelay(tt.value, tt.unit)
		if got != tt.want || valid != tt.valid {
			t.Fatalf("retryDelay(%q, %v) = (%v, %t), want (%v, %t)", tt.value, tt.unit, got, valid, tt.want, tt.valid)
		}
	}
}

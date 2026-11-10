package function

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStringArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
		err  string
	}{
		{"missing", nil, "", "missing value"},
		{"null", map[string]any{"value": nil}, "", "not a string"},
		{"number", map[string]any{"value": json.Number("1")}, "", "not a string"},
		{"boolean", map[string]any{"value": true}, "", "not a string"},
		{"empty", map[string]any{"value": ""}, "", ""},
		{"Unicode", map[string]any{"value": "世界🙂"}, "世界🙂", ""},
		{"controls", map[string]any{"value": "\n\"\\\x00"}, "\n\"\\\x00", ""},
		{"invalid UTF8", map[string]any{"value": "\xff"}, "", "invalid utf-8"},
		{"truncated rune", map[string]any{"value": "\xf0\x9f"}, "", "invalid utf-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := stringArgument(tc.args, "value")
			if tc.err == "" {
				if err != nil || got != tc.want {
					t.Fatalf("stringArgument = %q, %v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("error = %v, want %q", err, tc.err)
			}
			got, err = nonemptyStringArgument(tc.args, "value")
			if tc.err == "" && tc.want != "" {
				if err != nil || got != tc.want {
					t.Fatalf("nonemptyStringArgument = %q, %v", got, err)
				}
			} else if err == nil {
				t.Fatal("nonemptyStringArgument accepted invalid or empty string")
			}
		})
	}
}

func TestBoolArguments(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  bool
		valid bool
	}{
		{nil, false, true}, {false, false, true}, {true, true, true},
		{"true", false, false}, {json.Number("1"), false, false}, {[]any{}, false, false},
	} {
		got, err := boolArgument(map[string]any{"value": tc.value}, "value")
		if (err == nil) != tc.valid || got != tc.want {
			t.Fatalf("boolArgument(%#v) = %t, %v", tc.value, got, err)
		}
	}
	if got, err := boolArgument(nil, "value"); err != nil || got {
		t.Fatalf("missing bool = %t, %v", got, err)
	}
}

func TestPositiveIntArguments(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  int
		valid bool
	}{
		{nil, 7, true}, {json.Number("1"), 1, true}, {json.Number(strconv.Itoa(math.MaxInt)), math.MaxInt, true},
		{json.Number("0"), 0, false}, {json.Number("-1"), 0, false},
		{json.Number("1.0"), 0, false}, {json.Number("1e2"), 0, false},
		{json.Number("9223372036854775808"), 0, false}, {json.Number("invalid"), 0, false},
		{1, 0, false}, {float64(1), 0, false}, {"1", 0, false}, {true, 0, false},
	} {
		got, err := positiveIntArgument(map[string]any{"value": tc.value}, "value", 7)
		if (err == nil) != tc.valid || got != tc.want {
			t.Fatalf("positiveIntArgument(%#v) = %d, %v", tc.value, got, err)
		}
	}
	if got, err := positiveIntArgument(nil, "value", 7); err != nil || got != 7 {
		t.Fatalf("missing positive int = %d, %v", got, err)
	}
}

func TestNumberArguments(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  float64
		valid bool
	}{
		{nil, 7, true}, {json.Number("-0"), 0, true}, {json.Number("1.5"), 1.5, true},
		{json.Number("-2"), -2, true}, {json.Number("1.25e2"), 125, true},
		{json.Number("1e-9999"), 0, true}, {json.Number("1e9999"), 0, false},
		{json.Number("NaN"), 0, false}, {json.Number("Inf"), 0, false}, {json.Number("invalid"), 0, false},
		{float64(1), 0, false}, {"1", 0, false}, {false, 0, false},
	} {
		got, err := numberArgument(map[string]any{"value": tc.value}, "value", 7)
		if (err == nil) != tc.valid || (tc.valid && got != tc.want) {
			t.Fatalf("numberArgument(%#v) = %g, %v", tc.value, got, err)
		}
	}
	if got, err := numberArgument(nil, "value", 7); err != nil || got != 7 {
		t.Fatalf("missing number = %g, %v", got, err)
	}
	for _, value := range []any{json.Number("0"), json.Number("-0"), json.Number("-1"), "1", json.Number("NaN")} {
		if _, err := positiveNumberArgument(map[string]any{"value": value}, "value", 1); err == nil {
			t.Fatalf("accepted nonpositive/invalid value: %#v", value)
		}
	}
	if got, err := positiveNumberArgument(nil, "value", 1); err != nil || got != 1 {
		t.Fatalf("default positive number = %g, %v", got, err)
	}
	for _, value := range []string{"-1", "0", "1"} {
		if _, err := boundedNumberArgument(map[string]any{"value": json.Number(value)}, "value", 0, -1, 1); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []any{json.Number("-1.0001"), json.Number("1.0001"), true} {
		if _, err := boundedNumberArgument(map[string]any{"value": value}, "value", 0, -1, 1); err == nil {
			t.Fatalf("accepted invalid bounded number: %#v", value)
		}
	}
	if got, err := boundedNumberArgument(nil, "value", 0, -1, 1); err != nil || got != 0 {
		t.Fatalf("default bounded number = %g, %v", got, err)
	}
}

func FuzzStringArgument(f *testing.F) {
	for _, seed := range []string{"", "ASCII", "世界🙂", "\x00\n\"\\", "\xff", "\xf0\x9f"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got, err := stringArgument(map[string]any{"value": input}, "value")
		if (err == nil) != utf8.ValidString(input) || (err == nil && got != input) {
			t.Fatalf("stringArgument changed or misvalidated input: %q, %v", got, err)
		}
	})
}

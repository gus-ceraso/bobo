package provider

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type callbackFunction struct {
	call func(context.Context, map[string]any) []Part
}

func (callbackFunction) Schema() map[string]any {
	return map[string]any{"type": "function", "name": "inspect"}
}

func (f callbackFunction) Call(ctx context.Context, args map[string]any) []Part {
	return f.call(ctx, args)
}

func TestPromptFunctionArguments(t *testing.T) {
	for _, tt := range []struct {
		name      string
		arguments any
		want      map[string]any
	}{
		{"empty object", `{}`, map[string]any{}},
		{"whitespace", " \n {\"value\": 1} \t\r\n", map[string]any{"value": jsonv1.Number("1")}},
		{"large integer", `{"value":9007199254740993}`, map[string]any{"value": jsonv1.Number("9007199254740993")}},
		{"max int64", `{"value":9223372036854775807}`, map[string]any{"value": jsonv1.Number("9223372036854775807")}},
		{"min int64", `{"value":-9223372036854775808}`, map[string]any{"value": jsonv1.Number("-9223372036854775808")}},
		{"max uint64", `{"value":18446744073709551615}`, map[string]any{"value": jsonv1.Number("18446744073709551615")}},
		{"beyond uint64", `{"value":184467440737095516160}`, map[string]any{"value": jsonv1.Number("184467440737095516160")}},
		{"decimal precision", `{"value":0.123456789012345678901234567890}`, map[string]any{"value": jsonv1.Number("0.123456789012345678901234567890")}},
		{"exponent", `{"value":1.2300e+42}`, map[string]any{"value": jsonv1.Number("1.2300e+42")}},
		{"beyond float64", `{"value":1e9999}`, map[string]any{"value": jsonv1.Number("1e9999")}},
		{"below float64", `{"value":1e-9999}`, map[string]any{"value": jsonv1.Number("1e-9999")}},
		{"negative zero", `{"value":-0}`, map[string]any{"value": jsonv1.Number("-0")}},
		{"nested values", `{"value":[9007199254740993,{"number":-9007199254740993},[1.2500]]}`, map[string]any{
			"value": []any{jsonv1.Number("9007199254740993"), map[string]any{"number": jsonv1.Number("-9007199254740993")}, []any{jsonv1.Number("1.2500")}},
		}},
		{"other types", `{"text":"9007199254740993 世界🙂","boolean":true,"null":null,"array":[],"object":{}}`, map[string]any{
			"text": "9007199254740993 世界🙂", "boolean": true, "null": nil, "array": []any{}, "object": map[string]any{},
		}},
		{"empty input", ``, nil},
		{"whitespace only", " \n\t", nil},
		{"incomplete object", `{`, nil},
		{"trailing comma", `{"value":1,}`, nil},
		{"multiple objects", `{} {}`, nil},
		{"trailing value", `{} null`, nil},
		{"trailing garbage", `{} garbage`, nil},
		{"array", `[]`, nil},
		{"string", `"object"`, nil},
		{"boolean", `true`, nil},
		{"number", `1`, nil},
		{"null", `null`, nil},
		{"NaN", `{"value":NaN}`, nil},
		{"infinity", `{"value":Infinity}`, nil},
		{"leading zero", `{"value":01}`, nil},
		{"incomplete exponent", `{"value":1e}`, nil},
		{"duplicate names", `{"value":1,"value":2}`, nil},
		{"nested duplicate names", `{"outer":{"value":1,"value":2}}`, nil},
		{"unpaired high surrogate", `{"value":"\ud800"}`, nil},
		{"unpaired low surrogate", `{"value":"\udfff"}`, nil},
		{"missing arguments", nil, nil},
		{"non string arguments", true, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotArgs map[string]any
			var callContext context.Context
			calls := 0
			f := callbackFunction{call: func(ctx context.Context, args map[string]any) []Part {
				calls++
				gotArgs = args
				callContext = ctx
				return []Part{{ContentType: "text/plain", Content: []byte("ok")}}
			}}
			requests := 0
			c := mockClient(func(r *http.Request) (*http.Response, error) {
				requests++
				if requests == 1 {
					call := map[string]any{"type": "function_call", "call_id": "one", "name": "inspect"}
					if tt.arguments != nil {
						call["arguments"] = tt.arguments
					}
					return completedResponse([]map[string]any{call}), nil
				}
				if requests > 2 {
					return nil, errors.New("unexpected extra request")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var req struct {
					Input []map[string]any `json:"input"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatal(err)
				}
				if len(req.Input) != 3 {
					t.Fatalf("input = %#v, want prompt, call, and output", req.Input)
				}
				output := req.Input[2]
				if output["type"] != "function_call_output" || output["call_id"] != "one" {
					t.Fatalf("unexpected function output: %#v", output)
				}
				if tt.want == nil {
					if text, ok := output["output"].(string); !ok || !strings.HasPrefix(text, "invalid arguments:") {
						t.Errorf("invalid arguments did not produce an error: %#v", output)
					}
				} else if parts := decodeParts(output["output"]); len(parts) != 1 || string(parts[0].Content) != "ok" {
					t.Errorf("unexpected function result: %#v", output)
				}
				return completedResponse([]map[string]any{{"type": "message", "role": "assistant", "content": "done"}}), nil
			})
			s := NewSession("model", "low", "", []Function{f})
			got, err := c.Prompt(context.Background(), s, "hello")
			if err != nil || got != "done" || requests != 2 || len(s.Events) != 4 {
				t.Fatalf("Prompt() = %q, %v; requests = %d, events = %#v", got, err, requests, s.Events)
			}
			if tt.want == nil {
				if calls != 0 {
					t.Fatalf("function called %d times with invalid arguments", calls)
				}
			} else {
				if calls != 1 || !reflect.DeepEqual(gotArgs, tt.want) {
					t.Fatalf("function called %d times with %#v, want %#v", calls, gotArgs, tt.want)
				}
				if callContext == nil || !errors.Is(callContext.Err(), context.Canceled) {
					t.Fatalf("completed function context was not canceled: %v", callContext)
				}
			}
		})
	}
}

func FuzzDecodeArguments(f *testing.F) {
	for _, seed := range []string{
		`{}`, `null`, `[]`, `true`, `1`, `"text"`, ``, `{`, `{} {}`, `{} garbage`,
		`{"value":9007199254740993}`,
		`{"value":1e9999}`,
		`{"value":1e-9999}`,
		`{"value":[-0,1.2300e+42,{"nested":18446744073709551615}]}`,
		`{"value":null,"text":"世界🙂"}`,
		`{"value":1,"value":2}`,
		`{"value":"\ud800"}`,
		`{"value":"\ud83d\ude42"}`,
		"{\"value\":\"\xff\"}",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		args, err := decodeArguments(input)
		raw := jsontext.Value(input)
		validObject := raw.IsValid() && raw.Kind() == '{'
		if (err == nil) != validObject {
			t.Fatalf("decodeArguments(%q) = %#v, %v; valid object = %t", input, args, err, validObject)
		}
		if err != nil {
			if args != nil {
				t.Fatalf("invalid arguments returned a partial result: %#v", args)
			}
			return
		}
		if args == nil {
			t.Fatal("valid object decoded to nil arguments")
		}
		var checkTypes func(any)
		checkTypes = func(value any) {
			switch v := value.(type) {
			case nil, bool, string, jsonv1.Number:
			case []any:
				for _, item := range v {
					checkTypes(item)
				}
			case map[string]any:
				for _, item := range v {
					checkTypes(item)
				}
			default:
				t.Fatalf("unexpected argument type %T; numbers must remain json.Number", value)
			}
		}
		checkTypes(args)
		encoded, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("encoding decoded arguments: %v", err)
		}
		roundTrip, err := decodeArguments(string(encoded))
		if err != nil || !reflect.DeepEqual(roundTrip, args) {
			t.Fatalf("round trip = %#v, %v; want %#v", roundTrip, err, args)
		}
	})
}

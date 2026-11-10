package provider

import (
	"bytes"
	"encoding/base64"
	"reflect"
	"testing"
)

func TestEncodeParts(t *testing.T) {
	for _, tt := range []struct {
		name string
		part Part
		want map[string]any
	}{
		{"text", Part{"text/plain; charset=utf-8", []byte("hello")}, map[string]any{"type": "input_text", "text": "hello"}},
		{"detected text", Part{"not a media type", []byte("hello")}, map[string]any{"type": "input_text", "text": "hello"}},
		{"image", Part{"image/png", []byte{0, 1, 2}}, map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAEC"}},
		{"file", Part{"application/pdf", []byte("pdf")}, map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,cGRm"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := encodeParts([]Part{tt.part}); !reflect.DeepEqual(got, []any{tt.want}) {
				t.Fatalf("encodeParts() = %#v, want %#v", got, tt.want)
			}
		})
	}
	if got := encodeParts(nil); got == nil || len(got) != 0 {
		t.Fatalf("encodeParts(nil) = %#v, want empty non-nil array", got)
	}
}

func TestDecodeParts(t *testing.T) {
	text := func(s string) []Part { return []Part{{"text/plain", []byte(s)}} }
	for _, tt := range []struct {
		name  string
		input any
		want  []Part
	}{
		{"string", "hello", text("hello")},
		{"nil", nil, nil},
		{"wrong container", 42, nil},
		{"non object", []any{"ignored", nil}, nil},
		{"unknown type", []any{map[string]any{"type": "audio"}}, nil},
		{"input text", []any{map[string]any{"type": "input_text", "text": "in"}}, text("in")},
		{"output text", []any{map[string]any{"type": "output_text", "text": "out"}}, text("out")},
		{"refusal", []any{map[string]any{"type": "refusal", "refusal": "no"}}, text("no")},
		{"missing text", []any{map[string]any{"type": "output_text"}}, text("")},
		{"base64 image", []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAEC"}}, []Part{{"image/png", []byte{0, 1, 2}}}},
		{"base64 file", []any{map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,cGRm"}}, []Part{{"application/pdf", []byte("pdf")}}},
		{"escaped data", []any{map[string]any{"type": "input_file", "file_data": "data:text/plain,hello%20world+plus"}}, text("hello world+plus")},
		{"raw base64 file", []any{map[string]any{"type": "input_file", "file_data": "aGVsbG8="}}, []Part{{"text/plain; charset=utf-8", []byte("hello")}}},
		{"detected type", []any{map[string]any{"type": "input_file", "file_data": "data:;base64,aGVsbG8="}}, []Part{{"text/plain; charset=utf-8", []byte("hello")}}},
		{"missing data", []any{map[string]any{"type": "input_file"}}, nil},
		{"wrong data type", []any{map[string]any{"type": "input_image", "image_url": 42}}, nil},
		{"missing comma", []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64"}}, nil},
		{"remote image", []any{map[string]any{"type": "input_image", "image_url": "https://example.com/image.png"}}, nil},
		{"invalid base64", []any{map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,!"}}, nil},
		{"invalid raw base64", []any{map[string]any{"type": "input_file", "file_data": "!"}}, nil},
		{"invalid escape", []any{map[string]any{"type": "input_file", "file_data": "data:text/plain,%zz"}}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeParts(tt.input); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decodeParts() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestPartsRoundTrip(t *testing.T) {
	parts := []Part{
		{"text/plain", []byte("hello\n世界")},
		{"image/png", []byte{0, 1, 2, 255}},
		{"application/octet-stream", []byte{255, 0, 128}},
	}
	if got := decodeParts(encodeParts(parts)); !reflect.DeepEqual(got, parts) {
		t.Fatalf("round trip = %#v, want %#v", got, parts)
	}
	// Binary files without a media type are sniffed rather than treated as text.
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	encoded := encodeParts([]Part{{Content: png}})[0].(map[string]any)
	if encoded["type"] != "input_image" || encoded["image_url"] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("unexpected detected image: %#v", encoded)
	}
	if got := decodeParts([]any{encoded}); len(got) != 1 || !bytes.Equal(got[0].Content, png) {
		t.Fatalf("detected image round trip = %#v", got)
	}
}

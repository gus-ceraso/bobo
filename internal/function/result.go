package function

import (
	"fmt"
	"time"

	"ceraso.dev/bobo/internal/provider"
)

// toolResult retains metadata collected before a failure, including the call's
// start time. It encodes the existing tool protocol, not XML.
type toolResult struct {
	name       string
	attributes string
}

func newToolResult(name string) toolResult {
	return toolResult{name: name, attributes: fmt.Sprintf(" date=%q", time.Now().Format(time.RFC3339))}
}

func (r *toolResult) attribute(name string, value any) {
	r.attributes += fmt.Sprintf(" %s=%q", name, fmt.Sprint(value))
}

func textPart(text string) provider.Part {
	return provider.Part{ContentType: "text/plain", Content: []byte(text)}
}

func textResult(text string) []provider.Part { return []provider.Part{textPart(text)} }

func (r toolResult) header(err error) string {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return fmt.Sprintf("<%s%s error=%q", r.name, r.attributes, message)
}

func (r toolResult) status(err error) []provider.Part {
	return textResult(r.header(err) + "/>")
}

func (r toolResult) text(body string, err error) []provider.Part {
	return textResult(r.header(err) + ">\n" + body + "</" + r.name + ">")
}

func (r toolResult) image(body []byte) []provider.Part {
	return []provider.Part{
		textPart(r.header(nil) + ">\n"),
		{ContentType: "image/png", Content: body},
		textPart("\n</" + r.name + ">"),
	}
}

func previewText(text string) string {
	preview := fmt.Sprintf("%.80s", text)
	if len(preview) < len(text) {
		preview += "..."
	}
	return preview
}

package openai

import (
	"encoding/json"
	"time"
)

// Supported Responses item types. These aren't SSE notification types.
const (
	EventMessage            = "message"
	EventFunctionCall       = "function_call"
	EventFunctionCallOutput = "function_call_output"
	EventReasoning          = "reasoning"
	EventCompaction         = "compaction"
)

// Message roles. Roles are passed to the API unchanged.
const (
	RoleUser      = "user"
	RoleDeveloper = "developer"
	RoleAssistant = "assistant"
)

// Assistant message phases. An empty phase means no phase was supplied.
const (
	PhaseCommentary  = "commentary"
	PhaseFinalAnswer = "final_answer"
)

// Event is one Responses conversation item, not an entire model response or an
// incremental streaming notification. Treat recorded events as immutable.
// Database node IDs and parent references belong to the caller, not Event.
//
// To author input, create a message with an explicit Role, Text, and optional
// Images, or a function_call_output with ToolCallID, Text, and optional Images.
// Replay returned events with their Native payloads intact. Next gives all events
// in a returned batch the same local completion time. Set CreatedAt for
// caller-authored events; this metadata isn't sent to the API.
//
// Text contains visible message/refusal text, a readable function call, or tool
// result text. Reasoning uses summaries joined with blank lines, or "Model
// reasoning." when none are available. Compaction uses "Context compacted.";
// its encrypted payload isn't a readable summary.
//
// Images accepts image URLs or image data URLs, including image-only input with
// empty Text. Images keep their supplied order and use the API's default detail.
// The selected model must accept images. The client doesn't read local files,
// upload attachments, or fetch image URLs. Arbitrary file input and image
// generation aren't supported.
//
// Dispatch function calls using ToolName and ToolArguments, then record a result
// with the matching ToolCallID. Keep string results directly in Text; JSON-encode
// structured results. Denied executions need explicit, truthful text results.
//
// Returned events retain the server item in Native, minus the output-only
// created_by field. Native is authoritative for replay; Text and the tool/message
// fields are decoded views. Keep Native when persisting events and don't edit
// its views independently. When decoding persisted events, use
// json.Decoder.UseNumber so ToolArguments retains json.Number values.
type Event struct {
	CreatedAt     time.Time       `json:"created_at"` // Local event timestamp, not a provider timestamp.
	Type          string          `json:"type"`
	Text          string          `json:"text"`                     // Display text; actual input/output text for newly authored events.
	Role          string          `json:"role,omitempty"`           // Message role, passed to the API unchanged.
	Phase         string          `json:"phase,omitempty"`          // Original assistant phase.
	ToolCallID    string          `json:"tool_call_id,omitempty"`   // Tool call/result correlation ID, not an item ID.
	ToolName      string          `json:"tool_name,omitempty"`      // Function name for a function_call.
	ToolArguments map[string]any  `json:"tool_arguments,omitempty"` // Decoded function arguments; numbers are json.Number.
	Images        []string        `json:"images,omitempty"`         // Message or tool-result image URLs or image data URLs.
	Native        json.RawMessage `json:"native,omitempty"`         // Replay-ready server item, excluding output-only created_by.
}

// Session is a read-only snapshot of configuration and one selected history path.
// It isn't a tree, database handle, or running agent. Next neither modifies
// it nor appends to Events; the caller owns persistence and cursor selection.
// Don't mutate the snapshot or its referenced values during Next.
//
// Events must preserve complete model-output batches in their original order.
// Each function call needs one result before the next continuation; the API
// validates call/result pairing. Tools are JSON-serializable declarations,
// not runtime implementations. The caller owns any SQLite storage or tree
// machinery; cloning and rewinding select history but don't undo tool side effects.
//
// Steering is an ordinary user-message Event added before Next. It can't change
// input already sent to the API; queue it until the operation settles, or cancel
// the operation. Outstanding tool calls still need truthful results.
//
// Tool declarations contain name, description, and parameters. Names must be
// unique within the session. Next copies declarations into the fixed "functions"
// namespace without overriding their strict setting. If strict is omitted,
// the API chooses its default.
type Session struct {
	Model            string           `json:"model"`
	Effort           string           `json:"effort,omitempty"` // Model-supported reasoning effort; empty leaves the server default.
	Instructions     string           `json:"instructions,omitempty"`
	CompactThreshold int              `json:"compact_threshold,omitempty"` // Token threshold for native compaction; zero disables new compactions.
	Tools            []map[string]any `json:"tools,omitempty"`
	Events           []Event          `json:"events,omitempty"`
}

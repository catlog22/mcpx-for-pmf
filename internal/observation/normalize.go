package observation

import (
	"encoding/json"
)

// NormalizeToolInput converts MCP arguments into bounded, redacted JSON.
// Intent remains in the input for faithful request inspection; it is also
// stored in Event.Intent for JSON/diagnostic consumers. Text rendering uses
// the concrete command, path, and result instead of exposing the intent label.
func NormalizeToolInput(arguments map[string]any, maxBytes int) (json.RawMessage, bool) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	clean, truncated := Sanitize(arguments, maxBytes)
	encoded, err := json.Marshal(clean)
	if err != nil {
		return json.RawMessage(`{"value":"[REDACTED]"}`), true
	}
	return encoded, truncated
}

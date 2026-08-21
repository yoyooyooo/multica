package completionpolicy

import "encoding/json"

// Policy is the exact lifecycle policy for provider-driven issue completion.
type Policy string

const (
	LeafChildOnly Policy = "leaf_child_only"
	RecordOnly    Policy = "record_only"
	Unsupported   Policy = "unsupported"
)

// Parse is deliberately exact and fail closed. Missing metadata, a missing
// key, the exact empty string, and the exact leaf_child_only string select
// ordinary leaf-child completion. Every other JSON type or string is
// unsupported; no trimming or case folding occurs.
func Parse(raw []byte) Policy {
	if len(raw) == 0 {
		return LeafChildOnly
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return Unsupported
	}
	value, present := metadata["external_pr_completion_policy"]
	if !present {
		return LeafChildOnly
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return Unsupported
	}
	policy, ok := decoded.(string)
	if !ok {
		return Unsupported
	}
	switch policy {
	case "", string(LeafChildOnly):
		return LeafChildOnly
	case string(RecordOnly):
		return RecordOnly
	default:
		return Unsupported
	}
}

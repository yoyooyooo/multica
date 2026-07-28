package completionpolicy

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Policy
	}{
		{name: "empty metadata bytes", raw: "", want: LeafChildOnly},
		{name: "absent", raw: `{}`, want: LeafChildOnly},
		{name: "empty string", raw: `{"external_pr_completion_policy":""}`, want: LeafChildOnly},
		{name: "leaf child only", raw: `{"external_pr_completion_policy":"leaf_child_only"}`, want: LeafChildOnly},
		{name: "record only", raw: `{"external_pr_completion_policy":"record_only"}`, want: RecordOnly},
		{name: "unknown", raw: `{"external_pr_completion_policy":"future"}`, want: Unsupported},
		{name: "null", raw: `{"external_pr_completion_policy":null}`, want: Unsupported},
		{name: "bool", raw: `{"external_pr_completion_policy":true}`, want: Unsupported},
		{name: "number", raw: `{"external_pr_completion_policy":1}`, want: Unsupported},
		{name: "case variant", raw: `{"external_pr_completion_policy":"Leaf_Child_Only"}`, want: Unsupported},
		{name: "leading whitespace", raw: `{"external_pr_completion_policy":" leaf_child_only"}`, want: Unsupported},
		{name: "trailing whitespace", raw: `{"external_pr_completion_policy":"leaf_child_only "}`, want: Unsupported},
		{name: "bad json", raw: `{"external_pr_completion_policy":`, want: Unsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse([]byte(tc.raw)); got != tc.want {
				t.Fatalf("Parse(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

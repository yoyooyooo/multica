package deployment

import "testing"

func TestNormalizeKind(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Kind
	}{
		{name: "self host canonical", raw: "self_host", want: KindSelfHost},
		{name: "self host hyphen", raw: "self-host", want: KindSelfHost},
		{name: "self host compact", raw: "selfhost", want: KindSelfHost},
		{name: "cloud", raw: "cloud", want: KindCloud},
		{name: "managed", raw: "managed", want: KindCloud},
		{name: "dev", raw: "dev", want: KindDev},
		{name: "development", raw: "development", want: KindDev},
		{name: "empty", raw: "", want: KindUnknown},
		{name: "unknown", raw: "production", want: KindUnknown},
		{name: "trims and lowercases", raw: " SELF-HOST ", want: KindSelfHost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeKind(tt.raw); got != tt.want {
				t.Fatalf("NormalizeKind(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestIsSelfHostFromEnv(t *testing.T) {
	t.Setenv(KindEnv, "self_host")
	if !IsSelfHostFromEnv() {
		t.Fatal("IsSelfHostFromEnv() = false, want true")
	}

	t.Setenv(KindEnv, "cloud")
	if IsSelfHostFromEnv() {
		t.Fatal("IsSelfHostFromEnv() = true for cloud, want false")
	}
}

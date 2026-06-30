package sourcechannel

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const SchemaVersion = 1
const SourceOtherMaxRunes = 512

type Report struct {
	SchemaVersion int    `json:"schema_version"`
	Channel       string `json:"channel"`
	InstanceHash  string `json:"instance_hash"`
	SubjectHash   string `json:"subject_hash"`
	SourceOther   string `json:"source_other,omitempty"`
}

var validChannels = map[string]struct{}{
	"friends_colleagues": {},
	"search":             {},
	"social_x":           {},
	"social_linkedin":    {},
	"social_youtube":     {},
	"social_github":      {},
	"social_other":       {},
	"blog_newsletter":    {},
	"ai_assistant":       {},
	"from_work":          {},
	"event_conference":   {},
	"dont_remember":      {},
	"other":              {},
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func NormalizeChannel(channel string) string {
	return strings.ToLower(strings.TrimSpace(channel))
}

func ValidChannel(channel string) bool {
	_, ok := validChannels[NormalizeChannel(channel)]
	return ok
}

func NormalizeHash(hash string) string {
	return strings.ToLower(strings.TrimSpace(hash))
}

func ValidHash(hash string) bool {
	return hashPattern.MatchString(NormalizeHash(hash))
}

func NormalizeSourceOther(channel, sourceOther string) string {
	if NormalizeChannel(channel) != "other" {
		return ""
	}
	sourceOther = strings.TrimSpace(sourceOther)
	if sourceOther == "" {
		return ""
	}
	runes := []rune(sourceOther)
	if len(runes) > SourceOtherMaxRunes {
		sourceOther = string(runes[:SourceOtherMaxRunes])
	}
	return sourceOther
}

func InstanceHash(salt string) string {
	return hashHex(salt, "instance", "multica")
}

func SubjectHash(salt, userID string) string {
	return hashHex(salt, "subject", userID)
}

func hashHex(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

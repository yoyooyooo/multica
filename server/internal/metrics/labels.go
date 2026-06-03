package metrics

import (
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

const (
	labelSource         = "source"
	labelRuntimeMode    = "runtime_mode"
	labelProvider       = "provider"
	labelTerminalStatus = "terminal_status"
	labelFailureReason  = "failure_reason"
	labelTokenType      = "token_type"
	labelModel          = "model"
	labelModelAlias     = "model_alias"
)

var businessMetricLabels = map[string][]string{
	"multica_agent_task_enqueued_total":     {labelSource, labelRuntimeMode},
	"multica_agent_task_dispatched_total":   {labelSource, labelRuntimeMode},
	"multica_agent_task_started_total":      {labelSource, labelRuntimeMode, labelProvider},
	"multica_agent_task_terminal_total":     {labelSource, labelRuntimeMode, labelTerminalStatus},
	"multica_agent_task_failed_total":       {labelSource, labelRuntimeMode, labelFailureReason},
	"multica_agent_task_queue_wait_seconds": {labelSource, labelRuntimeMode},
	"multica_agent_task_run_seconds":        {labelSource, labelRuntimeMode, labelTerminalStatus},
	"multica_agent_task_total_seconds":      {labelSource, labelRuntimeMode, labelTerminalStatus},
	"multica_agent_task_in_progress":        {labelSource, labelRuntimeMode},
	"multica_agent_task_iteration_count":    {labelSource, labelTerminalStatus},
	"multica_llm_tokens_total":              {labelProvider, labelModel, labelTokenType, labelRuntimeMode, labelSource},
	"multica_llm_cost_usd_total":            {labelProvider, labelModel, labelTokenType, labelRuntimeMode, labelSource},
	"multica_llm_unpriced_tokens_total":     {labelProvider, labelModelAlias, labelTokenType},
	"multica_llm_request_total":             {labelProvider, labelModel, labelRuntimeMode},
	"multica_task_queued_expired_total":     {labelSource, labelRuntimeMode},
	"multica_task_lease_expired_total":      {labelSource},
}

var forbiddenMetricLabels = map[string]struct{}{
	"workspace_id": {},
	"user_id":      {},
	"agent_id":     {},
	"task_id":      {},
	"issue_id":     {},
	"runtime_id":   {},
	"session_id":   {},
	"ip":           {},
}

var (
	knownSources = map[string]string{
		"issue":           "issue",
		"chat":            "chat",
		"autopilot":       "autopilot",
		"autopilot_issue": "autopilot_issue",
		"quick_create":    "quick_create",
		"manual":          "manual",
		"api":             "api",
		"other":           "other",
	}
	knownRuntimeModes = map[string]string{
		"local":   "local",
		"cloud":   "cloud",
		"unknown": "unknown",
	}
	knownRuntimeProviders = map[string]string{
		"antigravity":   "antigravity",
		"claude":        "claude",
		"codex":         "codex",
		"copilot":       "copilot",
		"cursor":        "cursor",
		"gemini":        "gemini",
		"hermes":        "hermes",
		"kiro":          "kiro",
		"kimi":          "kimi",
		"multica_agent": "multica_agent",
		"openclaw":      "openclaw",
		"opencode":      "opencode",
		"pi":            "pi",
		"other":         "other",
	}
	knownTerminalStatuses = map[string]string{
		"completed": "completed",
		"failed":    "failed",
		"cancelled": "cancelled",
		"blocked":   "blocked",
		"other":     "other",
	}
	knownTokenTypes = map[string]string{
		"input":       "input",
		"output":      "output",
		"cache_read":  "cache_read",
		"cache_write": "cache_write",
	}
	knownFailureReasons = map[string]string{}
	modelAliasUnsafeRe  = regexp.MustCompile(`[^a-z0-9._:/+-]+`)
)

func init() {
	for _, reason := range taskfailure.AllReasons() {
		knownFailureReasons[reason.String()] = reason.String()
	}
}

func validateBusinessMetricLabels() {
	for metric, labels := range businessMetricLabels {
		for _, label := range labels {
			if _, forbidden := forbiddenMetricLabels[label]; forbidden {
				panic("forbidden high-cardinality label " + label + " on " + metric)
			}
		}
	}
}

func metricLabels(metric string) []string {
	labels, ok := businessMetricLabels[metric]
	if !ok {
		panic("missing business metric label definition for " + metric)
	}
	return labels
}

func NormalizeTaskSource(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownSources[value]; ok {
		return normalized
	}
	return "other"
}

func NormalizeRuntimeMode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownRuntimeModes[value]; ok {
		return normalized
	}
	return "unknown"
}

func NormalizeRuntimeProvider(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownRuntimeProviders[value]; ok {
		return normalized
	}
	return "other"
}

func NormalizeTerminalStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownTerminalStatuses[value]; ok {
		return normalized
	}
	return "other"
}

func NormalizeFailureReason(value string) string {
	value = strings.TrimSpace(value)
	if normalized, ok := knownFailureReasons[value]; ok {
		return normalized
	}
	return taskfailure.Classify(value).String()
}

func NormalizeTokenType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownTokenTypes[value]; ok {
		return normalized
	}
	return "input"
}

func NormalizeModelAlias(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	value = modelAliasUnsafeRe.ReplaceAllString(value, "_")
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

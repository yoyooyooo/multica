package metrics

import (
	"strconv"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func TestBusinessMetricsLifecycleCountersAndGauge(t *testing.T) {
	m := NewBusinessMetrics()

	m.RecordTaskEnqueued("issue", "local")
	for i := 0; i < 100; i++ {
		m.RecordTaskDispatched("task-"+strconv.Itoa(i), "issue", "local", 2.5)
	}
	m.RecordTaskStarted("issue", "local", "codex")
	m.RecordTaskTerminal("task-0", "issue", "local", "completed", 10, 20, 1)

	if got := testutil.ToFloat64(m.taskEnqueued.WithLabelValues("issue", "local")); got != 1 {
		t.Fatalf("enqueued counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.taskDispatched.WithLabelValues("issue", "local")); got != 100 {
		t.Fatalf("dispatched counter = %v, want 100", got)
	}
	if got := testutil.ToFloat64(m.taskStarted.WithLabelValues("issue", "local", "codex")); got != 1 {
		t.Fatalf("started counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.taskTerminal.WithLabelValues("issue", "local", "completed")); got != 1 {
		t.Fatalf("terminal counter = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(m.taskInProgress); got != 1 {
		t.Fatalf("in_progress series count = %d, want 1 despite 100 task ids", got)
	}
	if got := testutil.ToFloat64(m.taskInProgress.WithLabelValues("issue", "local")); got != 99 {
		t.Fatalf("in_progress gauge = %v, want 99", got)
	}
	if got := testutil.CollectAndCount(m.taskQueueWait); got != 1 {
		t.Fatalf("queue wait series count = %d, want 1", got)
	}
	if got := testutil.CollectAndCount(m.taskRunSeconds); got != 1 {
		t.Fatalf("run seconds series count = %d, want 1", got)
	}
	if got := testutil.CollectAndCount(m.taskTotalSeconds); got != 1 {
		t.Fatalf("total seconds series count = %d, want 1", got)
	}
}

func TestBusinessMetricsFailureReasonUsesCanonicalClassifier(t *testing.T) {
	m := NewBusinessMetrics()

	rawError := `API Error: 429 {"error":"overloaded"}`
	m.RecordTaskFailed("issue", "local", rawError)

	wantReason := taskfailure.ReasonAgentProviderCapacityOrRateLimit.String()
	if got := testutil.ToFloat64(m.taskFailed.WithLabelValues("issue", "local", wantReason)); got != 1 {
		t.Fatalf("classified failure counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.taskFailed.WithLabelValues("issue", "local", taskfailure.ReasonAgentUnknown.String())); got != 0 {
		t.Fatalf("unknown failure counter = %v, want 0", got)
	}
}

func TestBusinessMetricsLLMPricingAndUnpricedTokens(t *testing.T) {
	m := NewBusinessMetrics()

	m.RecordLLMUsage("chat", "cloud", "codex", "gpt-5.4", 1_000_000, 2_000_000, 3_000_000, 4_000_000)

	if got := testutil.ToFloat64(m.llmTokens.WithLabelValues("openai", "gpt-5.4", "input", "cloud", "chat")); got != 1_000_000 {
		t.Fatalf("priced input tokens = %v, want 1000000", got)
	}
	if got := testutil.ToFloat64(m.llmTokens.WithLabelValues("openai", "gpt-5.4", "output", "cloud", "chat")); got != 2_000_000 {
		t.Fatalf("priced output tokens = %v, want 2000000", got)
	}
	if got := testutil.ToFloat64(m.llmCostUSD.WithLabelValues("openai", "gpt-5.4", "input", "cloud", "chat")); got != 2.5 {
		t.Fatalf("priced input cost = %v, want 2.5", got)
	}
	if got := testutil.ToFloat64(m.llmCostUSD.WithLabelValues("openai", "gpt-5.4", "output", "cloud", "chat")); got != 30 {
		t.Fatalf("priced output cost = %v, want 30", got)
	}
	if got := testutil.ToFloat64(m.llmRequests.WithLabelValues("openai", "gpt-5.4", "cloud")); got != 1 {
		t.Fatalf("priced request counter = %v, want 1", got)
	}

	m.RecordLLMUsage("issue", "local", "custom-provider", "Free Model!!", 7, 0, 0, 0)
	if got := testutil.ToFloat64(m.llmUnpricedTokens.WithLabelValues("other", "free_model_", "input")); got != 7 {
		t.Fatalf("unpriced input tokens = %v, want 7", got)
	}
	if got := testutil.ToFloat64(m.llmRequests.WithLabelValues("other", "unknown", "local")); got != 1 {
		t.Fatalf("unpriced request counter = %v, want 1", got)
	}
}

func TestBusinessMetricsRegistryExposesAllFamilies(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewBusinessMetrics()
	registry.MustRegister(m.Collectors()...)

	m.RecordTaskEnqueued("issue", "local")
	m.RecordTaskDispatched("task-1", "issue", "local", 1)
	m.RecordTaskStarted("issue", "local", "codex")
	m.RecordTaskTerminal("task-1", "issue", "local", "completed", 2, 3, 1)
	m.RecordTaskFailed("issue", "local", taskfailure.ReasonTimeout.String())
	m.RecordTaskQueuedExpired("issue", "local")
	m.RecordTaskLeaseExpired("issue")
	m.RecordLLMUsage("issue", "local", "codex", "gpt-5.4", 1, 1, 1, 1)
	m.RecordLLMUsage("issue", "local", "custom-provider", "custom-model", 1, 0, 0, 0)

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	seen := make(map[string]bool, len(families))
	for _, family := range families {
		seen[family.GetName()] = true
	}
	for metric := range businessMetricLabels {
		if !seen[metric] {
			t.Fatalf("registry did not expose metric family %s", metric)
		}
	}
}

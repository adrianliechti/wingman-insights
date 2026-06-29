package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestAllQueriesExecute runs every public Query* method against a seeded DB with
// both an empty filter and a fully-populated one. SQL column/binding errors (e.g.
// a stale service_name reference, or an app_id clause bound against a table that
// lacks the column) are runtime-only — the compiler can't see them — so this
// executes the real SQL of every view to catch them.
func TestAllQueriesExecute(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	ctx := context.Background()

	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	seedAll(ctx, t, s)

	to := time.Now().UTC()
	from := to.Add(-48 * time.Hour)
	const iv = "1 hour"

	filters := map[string]Filter{
		"empty": {},
		"full": {
			App: "checkout", User: "u1", Department: "Eng", Location: "Zurich",
			Provider: "anthropic", Models: []string{"claude"},
		},
	}

	for fname, f := range filters {
		// name -> call; every call must run without a SQL error.
		calls := map[string]func() error{
			"TokenAnomalies/user":  func() error { _, e := s.QueryTokenAnomalies(ctx, from, to, iv, "user", 0, f); return e },
			"TokenAnomalies/model": func() error { _, e := s.QueryTokenAnomalies(ctx, from, to, iv, "model", 0, f); return e },
			"TokenAnomalies/none":  func() error { _, e := s.QueryTokenAnomalies(ctx, from, to, iv, "none", 0, f); return e },
			"CostAnomalies/app":    func() error { _, e := s.QueryCostAnomalies(ctx, from, to, iv, "app", 0, f); return e },
			"CostAnomalies/model":  func() error { _, e := s.QueryCostAnomalies(ctx, from, to, iv, "model", 0, f); return e },
			"CostAnomalies/none":   func() error { _, e := s.QueryCostAnomalies(ctx, from, to, iv, "none", 0, f); return e },
			"AnomalyFeed":          func() error { _, e := s.QueryAnomalyFeed(ctx, from, to, iv, 0, 50, f); return e },
			"CostBreakdown":        func() error { _, e := s.QueryCostBreakdown(ctx, from, to, f); return e },
			"CostTimeseries/app":   func() error { _, e := s.QueryCostTimeseries(ctx, from, to, iv, "app", f); return e },
			"CostTimeseries/model": func() error { _, e := s.QueryCostTimeseries(ctx, from, to, iv, "model", f); return e },
			"TokenVolumeTS/app":    func() error { _, e := s.QueryTokenVolumeTimeseries(ctx, from, to, iv, "app", f); return e },
			"TokenVolumeTS/model":  func() error { _, e := s.QueryTokenVolumeTimeseries(ctx, from, to, iv, "model", f); return e },
			"HTTPSummary":          func() error { _, e := s.QueryHTTPSummary(ctx, from, to, f); return e },
			"HTTPTimeseries":       func() error { _, e := s.QueryHTTPTimeseries(ctx, from, to, iv, f); return e },
			"HTTPRequestsTS":       func() error { _, e := s.QueryHTTPRequestsTimeseries(ctx, from, to, iv, f); return e },
			"HTTPErrorsByCode":     func() error { _, e := s.QueryHTTPErrorsByCode(ctx, from, to, f); return e },
			"FilterOptions":        func() error { _, e := s.QueryFilterOptions(ctx, from, to); return e },
			"TokenSummary":         func() error { _, e := s.QueryTokenSummary(ctx, from, to, f); return e },
			"TokenTimeseries":      func() error { _, e := s.QueryTokenTimeseries(ctx, from, to, iv, f); return e },
			"OperationSummary":     func() error { _, e := s.QueryOperationSummary(ctx, from, to, f); return e },
			"TopConsumers":         func() error { _, e := s.QueryTopConsumers(ctx, from, to, 10, f); return e },
			"ActiveUsers":          func() error { _, e := s.QueryActiveUsers(ctx, to, f); return e },
			"OpDurationTS":         func() error { _, e := s.QueryOperationDurationTimeseries(ctx, from, to, iv, f); return e },
			"ModelDistribution":    func() error { _, e := s.QueryModelDistribution(ctx, from, to, f); return e },
			"CacheEfficiency":      func() error { _, e := s.QueryCacheEfficiency(ctx, from, to, iv, f); return e },
			"ReasoningShare":       func() error { _, e := s.QueryReasoningShare(ctx, from, to, iv, f); return e },
			"TokenComposition":     func() error { _, e := s.QueryTokenComposition(ctx, from, to, iv, f); return e },
			"GenAIErrors":          func() error { _, e := s.QueryGenAIErrors(ctx, from, to, f); return e },
			"TraceList":            func() error { _, e := s.QueryTraceList(ctx, from, to, f, false, 50); return e },
			"Trace":                func() error { _, e := s.QueryTrace(ctx, "trace-1"); return e },
			"ModelMixTS":           func() error { _, e := s.QueryModelMixTimeseries(ctx, from, to, iv, f); return e },
			"OperationMixTS":       func() error { _, e := s.QueryOperationMixTimeseries(ctx, from, to, iv, f); return e },
			"TokensPerRequest":     func() error { _, e := s.QueryTokensPerRequest(ctx, from, to, iv, f); return e },
			"SessionsTS":           func() error { _, e := s.QuerySessionsTimeseries(ctx, from, to, iv, f); return e },
			"SessionStats":         func() error { _, e := s.QuerySessionStats(ctx, from, to, f); return e },
			"LatencyPercentiles":   func() error { _, e := s.QueryLatencyPercentiles(ctx, from, to, iv, f); return e },
			"TTFCTimeseries":       func() error { _, e := s.QueryTTFCTimeseries(ctx, from, to, iv, f); return e },
			"GenAIErrorRate":       func() error { _, e := s.QueryGenAIErrorRate(ctx, from, to, iv, f); return e },
			"ToolStats":            func() error { _, e := s.QueryToolStats(ctx, from, to, f); return e },
			"Interactions":         func() error { _, e := s.QueryInteractions(ctx, from, to, f); return e },
			"ThroughputTS":         func() error { _, e := s.QueryThroughputTimeseries(ctx, from, to, iv, f); return e },
			"UserStats":            func() error { _, e := s.QueryUserStats(ctx, from, to, 10, f); return e },
			"UserSegments":         func() error { _, e := s.QueryUserSegments(ctx, from, to, f); return e },
			"CohortRetention":      func() error { _, e := s.QueryCohortRetention(ctx, to, 4, f); return e },
			"AppAdoption":          func() error { _, e := s.QueryAppAdoption(ctx, from, to, f); return e },
			"ModelPreference":      func() error { _, e := s.QueryModelPreferenceBySegment(ctx, from, to, f); return e },
			"UserBurst":            func() error { _, e := s.QueryUserBurst(ctx, from, to, 10, f); return e },
			"NewVsReturningTS":     func() error { _, e := s.QueryNewVsReturningTimeseries(ctx, from, to, iv, f); return e },
		}
		for name, call := range calls {
			if err := call(); err != nil {
				t.Errorf("[filter=%s] %s: %v", fname, name, err)
			}
		}
	}
}

func seedAll(ctx context.Context, t *testing.T, s *Store) {
	t.Helper()
	now := time.Now().UTC().Add(-time.Hour)
	if err := s.InsertSpans(ctx, []SpanRow{{
		ReceivedAt: now, Time: now, Duration: 1.2, TraceID: "trace-1", SpanID: "span-1",
		Name: "chat", Kind: "client", Status: "ok", AppID: "checkout",
		OperationName: "chat", ProviderName: "anthropic", RequestModel: "claude",
		ResponseModel: "claude", AgentName: "assistant", ToolName: "search",
		UserID: "u1", UserEmail: "u1@corp.com", SessionID: "sess-1",
		InputTokens: 100, OutputTokens: 50, CacheRead: 10, CacheCreation: 5, Reasoning: 8,
	}}); err != nil {
		t.Fatalf("seed spans: %v", err)
	}
	if err := s.InsertGenAIMetrics(ctx, []GenAIMetricRow{{
		ReceivedAt: now, Time: now, ServiceName: "wingman", MetricName: "gen_ai.client.token.usage",
		OperationName: "chat", ProviderName: "anthropic", RequestModel: "claude", TokenType: "input",
		EndUserID: "u1", EndUserEmail: "u1@corp.com", Count: 1, Sum: 100,
	}}); err != nil {
		t.Fatalf("seed genai metrics: %v", err)
	}
	if err := s.InsertHTTPMetrics(ctx, []HTTPMetricRow{{
		ReceivedAt: now, Time: now, ServiceName: "wingman", MetricName: "http.server.request.duration",
		Direction: "inbound", Method: "POST", Route: "/v1/messages", StatusCode: 200, Count: 1, Sum: 0.3,
	}}); err != nil {
		t.Fatalf("seed http metrics: %v", err)
	}
}

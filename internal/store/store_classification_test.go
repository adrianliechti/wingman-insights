package store

import (
	"context"
	"iter"
	"path/filepath"
	"testing"
	"time"

	"insights/pkg/directory"
)

// classificationFixture inserts one window's worth of classification data points
// plus the token-usage rows that carry each conversation's user — which is how
// the classification queries attribute risks to people, since the
// classification metric itself carries no principal.
func classificationFixture(t *testing.T) (*Store, context.Context) {
	t.Helper()
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	ctx := context.Background()

	s, err := New()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC().Add(-time.Hour)
	cls := func(session, kind, id string, matched bool, count int64, scoreSum float64) GenAIMetricRow {
		m := "false"
		if matched {
			m = "true"
		}
		return GenAIMetricRow{
			ReceivedAt: now, Time: now, ServiceName: "wingman", AppID: "wingman-chat",
			MetricName: classificationMetric, OperationName: "evaluate",
			RequestModel: "claude-haiku-4-5", SessionID: session,
			Count: count, Sum: scoreSum,
			Attributes: map[string]string{
				"wingman.classification.kind":      kind,
				"wingman.classification.id":        id,
				"wingman.classification.matched":   m,
				"wingman.classification.threshold": "0.6",
				"gen_ai.conversation.id":           session,
			},
		}
	}
	usage := func(session, user string) GenAIMetricRow {
		return GenAIMetricRow{
			ReceivedAt: now, Time: now, ServiceName: "wingman", AppID: "chat-api",
			MetricName: "gen_ai.client.token.usage", OperationName: "chat",
			RequestModel: "gpt-4o", TokenType: "input", SessionID: session,
			EndUserID: user, EndUserEmail: user + "@corp.com", Count: 1, Sum: 100,
		}
	}

	if err := s.InsertGenAIMetrics(ctx, []GenAIMetricRow{
		usage("conv-a", "alice"),
		usage("conv-b", "bob"),

		// conv-a: 4 legal prompts, pii fired on 2 of them.
		cls("conv-a", "category", "legal", true, 4, 3.6),
		cls("conv-a", "risk", "pii", true, 2, 1.7),
		cls("conv-a", "risk", "pii", false, 2, 0),
		cls("conv-a", "risk", "credit", false, 4, 0),

		// conv-b: 1 hr prompt, credit fired on it.
		cls("conv-b", "category", "hr", true, 1, 0.7),
		cls("conv-b", "risk", "pii", false, 1, 0),
		cls("conv-b", "risk", "credit", true, 1, 0.95),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return s, ctx
}

func classificationWindow() (time.Time, time.Time) {
	to := time.Now().UTC()
	return to.Add(-48 * time.Hour), to
}

// TestClassificationSummary verifies the aggregation math: evaluations sum data
// point counts, matched sums only the "true" points, and the average score is
// taken over the matched evaluations only — an unmatched risk reports a score of
// 0, so averaging over all of them would understate how confident the triggers
// were (and merely restate the trigger rate).
func TestClassificationSummary(t *testing.T) {
	s, ctx := classificationFixture(t)
	from, to := classificationWindow()

	got, err := s.QueryClassificationSummary(ctx, from, to, Filter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	byKey := map[string]ClassificationRow{}
	for _, r := range got {
		byKey[r.Kind+"/"+r.ID] = r
	}

	pii := byKey["risk/pii"]
	if pii.Evaluations != 5 || pii.Matched != 2 {
		t.Errorf("risk pii: evaluations=%d matched=%d, want 5/2", pii.Evaluations, pii.Matched)
	}
	if want := 2.0 / 5.0; pii.MatchRate < want-1e-9 || pii.MatchRate > want+1e-9 {
		t.Errorf("risk pii match rate = %v, want %v", pii.MatchRate, want)
	}
	if pii.AvgScore < 0.85-1e-9 || pii.AvgScore > 0.85+1e-9 { // 1.7 / 2 matched, not / 5
		t.Errorf("risk pii avg score = %v, want 0.85", pii.AvgScore)
	}
	if pii.Threshold != 0.6 {
		t.Errorf("risk pii threshold = %v, want 0.6", pii.Threshold)
	}
	if pii.LastMatched == nil {
		t.Error("risk pii last matched = nil, want a timestamp")
	}
	if credit := byKey["risk/credit"]; credit.Evaluations != 5 || credit.Matched != 1 {
		t.Errorf("risk credit: evaluations=%d matched=%d, want 5/1", credit.Evaluations, credit.Matched)
	}
	if legal := byKey["category/legal"]; legal.Evaluations != 4 || legal.Matched != 4 {
		t.Errorf("category legal: evaluations=%d matched=%d, want 4/4", legal.Evaluations, legal.Matched)
	}
}

// TestClassificationStats checks the headline numbers: prompts come from the
// winning category (exactly one matches per prompt) and must not be multiplied
// by the number of configured risks, while triggers count matched risks only.
func TestClassificationStats(t *testing.T) {
	s, ctx := classificationFixture(t)
	from, to := classificationWindow()

	st, err := s.QueryClassificationStats(ctx, from, to, Filter{})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Prompts != 5 {
		t.Errorf("prompts = %d, want 5", st.Prompts)
	}
	if st.Triggers != 3 {
		t.Errorf("triggers = %d, want 3", st.Triggers)
	}
	if st.RiskEvaluations != 10 {
		t.Errorf("risk evaluations = %d, want 10", st.RiskEvaluations)
	}
	if st.Conversations != 2 {
		t.Errorf("conversations = %d, want 2", st.Conversations)
	}
	if st.Users != 2 {
		t.Errorf("users = %d, want 2 (resolved through their conversations)", st.Users)
	}
	if want := 60.0; st.TriggersPerHundred < want-1e-9 || st.TriggersPerHundred > want+1e-9 {
		t.Errorf("triggers per hundred = %v, want %v", st.TriggersPerHundred, want)
	}
}

// TestClassificationUserFilter verifies risks are attributed to the user of the
// conversation they belong to (the classification metric carries no principal of
// its own), and that the User filter narrows through that same derived identity.
func TestClassificationUserFilter(t *testing.T) {
	s, ctx := classificationFixture(t)
	from, to := classificationWindow()

	// Unfiltered: both conversations' triggers are present (pii + credit).
	all, err := s.QueryClassificationStats(ctx, from, to, Filter{})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if all.Triggers != 3 || all.Users != 2 {
		t.Fatalf("unfiltered stats = %+v, want 3 triggers / 2 users", all)
	}

	// Filtering to alice (who only exists on conv-a) must drop conv-b's credit
	// trigger, proving the User filter resolves through the conversation.
	alice, err := s.QueryClassificationStats(ctx, from, to, Filter{User: []string{"alice@corp.com"}})
	if err != nil {
		t.Fatalf("stats filtered: %v", err)
	}
	if alice.Triggers != 2 || alice.Users != 1 || alice.Prompts != 4 {
		t.Errorf("alice stats = %+v, want 2 triggers / 1 user / 4 prompts", alice)
	}
}

// TestClassificationMatrix verifies that risk triggers are attributed to the
// dominant topic of their conversation, each trigger counted exactly once.
func TestClassificationMatrix(t *testing.T) {
	s, ctx := classificationFixture(t)
	from, to := classificationWindow()

	cells, err := s.QueryClassificationMatrix(ctx, from, to, Filter{})
	if err != nil {
		t.Fatalf("matrix: %v", err)
	}
	got := map[string]ClassificationMatrixCell{}
	var total int64
	for _, c := range cells {
		got[c.Category+"/"+c.Risk] = c
		total += c.Triggers
	}
	if total != 3 {
		t.Errorf("matrix triggers = %d, want 3 (no double counting)", total)
	}
	if c := got["legal/pii"]; c.Triggers != 2 || c.Prompts != 4 {
		t.Errorf("legal/pii = %+v, want 2 triggers of 4 prompts", c)
	}
	if c := got["hr/credit"]; c.Triggers != 1 || c.Prompts != 1 {
		t.Errorf("hr/credit = %+v, want 1 trigger of 1 prompt", c)
	}
}

// TestClassificationRiskTimeseries checks that only triggers are plotted, one
// series per risk: the unmatched evaluations are a constant per prompt and would
// plot configuration (prompts × configured risks) rather than behaviour.
func TestClassificationRiskTimeseries(t *testing.T) {
	s, ctx := classificationFixture(t)
	from, to := classificationWindow()

	points, err := s.QueryClassificationRiskTimeseries(ctx, from, to, "1 hour", Filter{})
	if err != nil {
		t.Fatalf("risk timeseries: %v", err)
	}
	byRisk := map[string]float64{}
	for _, p := range points {
		byRisk[p.Label] += p.Value
	}
	if byRisk["pii"] != 2 || byRisk["credit"] != 1 || len(byRisk) != 2 {
		t.Errorf("risk series = %v, want pii=2 credit=1 only", byRisk)
	}
}

// classDeptStub resolves the fixture's two users to departments so the
// department comparison has something to group by: alice -> Engineering,
// bob -> Legal & Compliance. Everyone else is unknown (folds into "").
type classDeptStub struct{}

func (classDeptStub) Lookup(id string) (directory.Identity, bool) {
	switch id {
	case "alice@corp.com":
		return directory.Identity{ID: "alice@corp.com", Name: "Alice", Kind: directory.KindUser, Department: "Engineering", Location: "Zurich"}, true
	case "bob@corp.com":
		return directory.Identity{ID: "bob@corp.com", Name: "Bob", Kind: directory.KindUser, Department: "Legal & Compliance", Location: "Vaduz"}, true
	}
	return directory.Identity{}, false
}

func (classDeptStub) Records() iter.Seq[directory.Record] {
	return func(yield func(directory.Record) bool) {
		recs := []directory.Record{
			{Alias: "alice@corp.com", ID: "alice@corp.com", Name: "Alice", Kind: directory.KindUser, Department: "Engineering", Location: "Zurich"},
			{Alias: "bob@corp.com", ID: "bob@corp.com", Name: "Bob", Kind: directory.KindUser, Department: "Legal & Compliance", Location: "Vaduz"},
		}
		for _, r := range recs {
			if !yield(r) {
				return
			}
		}
	}
}

// TestClassificationByDepartment verifies risk triggers are rolled up by the
// resolved department of the conversation's user: alice (Engineering) carries
// conv-a's 2 pii triggers over 4 prompts, bob (Legal & Compliance) conv-b's 1
// credit trigger over 1 prompt.
func TestClassificationByDepartment(t *testing.T) {
	s, ctx := classificationFixture(t)
	s.SetDirectory(classDeptStub{})
	if err := s.SyncDirectory(ctx); err != nil {
		t.Fatalf("sync directory: %v", err)
	}
	from, to := classificationWindow()

	rows, err := s.QueryClassificationByDepartment(ctx, from, to, Filter{})
	if err != nil {
		t.Fatalf("by department: %v", err)
	}
	byDept := map[string]ClassificationDepartmentRow{}
	for _, r := range rows {
		byDept[r.Department] = r
	}

	eng := byDept["Engineering"]
	if eng.Users != 1 || eng.Prompts != 4 || eng.Triggers != 2 || eng.TopRisk != "pii" {
		t.Errorf("Engineering = %+v, want 1 user / 4 prompts / 2 triggers / pii", eng)
	}
	if want := 50.0; eng.TriggersPerHundred < want-1e-9 || eng.TriggersPerHundred > want+1e-9 {
		t.Errorf("Engineering per-100 = %v, want %v", eng.TriggersPerHundred, want)
	}
	if legal := byDept["Legal & Compliance"]; legal.Prompts != 1 || legal.Triggers != 1 || legal.TopRisk != "credit" {
		t.Errorf("Legal & Compliance = %+v, want 1 prompt / 1 trigger / credit", legal)
	}
}

// TestClassificationTopicTimeseries checks that the topic trend plots matched
// category prompts only, one series per topic, summing to the prompt volume.
func TestClassificationTopicTimeseries(t *testing.T) {
	s, ctx := classificationFixture(t)
	from, to := classificationWindow()

	points, err := s.QueryClassificationTopicTimeseries(ctx, from, to, "1 hour", Filter{})
	if err != nil {
		t.Fatalf("topic timeseries: %v", err)
	}
	byTopic := map[string]float64{}
	var total float64
	for _, p := range points {
		byTopic[p.Label] += p.Value
		total += p.Value
	}
	if byTopic["legal"] != 4 || byTopic["hr"] != 1 || len(byTopic) != 2 {
		t.Errorf("topic series = %v, want legal=4 hr=1 only", byTopic)
	}
	// The series must sum to the headline prompt count — one category per prompt.
	if total != 5 {
		t.Errorf("topic series total = %v, want 5 (the prompt volume)", total)
	}
}

// TestClassificationDepartmentTopics verifies each department's prompts are
// broken down by winning topic, and that a department's cells sum to the prompt
// volume QueryClassificationByDepartment reports for it.
func TestClassificationDepartmentTopics(t *testing.T) {
	s, ctx := classificationFixture(t)
	s.SetDirectory(classDeptStub{})
	if err := s.SyncDirectory(ctx); err != nil {
		t.Fatalf("sync directory: %v", err)
	}
	from, to := classificationWindow()

	cells, err := s.QueryClassificationDepartmentTopics(ctx, from, to, 0, Filter{})
	if err != nil {
		t.Fatalf("department topics: %v", err)
	}
	got := map[string]int64{}
	for _, c := range cells {
		got[c.Department+"/"+c.Category] = c.Prompts
	}
	if got["Engineering/legal"] != 4 {
		t.Errorf("Engineering/legal = %d, want 4", got["Engineering/legal"])
	}
	if got["Legal & Compliance/hr"] != 1 {
		t.Errorf("Legal & Compliance/hr = %d, want 1", got["Legal & Compliance/hr"])
	}
	if len(got) != 2 {
		t.Errorf("cells = %v, want exactly 2 (one topic per department)", got)
	}
}

// TestFoldDepartmentTopicTail covers the top-N cap that keeps the topic-mix
// chart plottable for a tenant with hundreds of departments: the leading
// departments survive intact, everything below is summarized into one Other
// group, and no prompt is lost or double counted in the process.
func TestFoldDepartmentTopicTail(t *testing.T) {
	// 5 departments of descending volume, two topics each.
	var cells []ClassificationDepartmentTopicCell
	vol := map[string]int64{"big": 100, "mid": 60, "small": 30, "tiny": 10, "micro": 4}
	for d, v := range vol {
		cells = append(cells,
			ClassificationDepartmentTopicCell{Department: d, Category: "legal", Prompts: v * 3 / 4},
			ClassificationDepartmentTopicCell{Department: d, Category: "hr", Prompts: v - v*3/4},
		)
	}
	total := func(in []ClassificationDepartmentTopicCell) int64 {
		var n int64
		for _, c := range in {
			n += c.Prompts
		}
		return n
	}
	want := total(cells)

	// Under the cap: returned untouched.
	if got := foldDepartmentTopicTail(cells, 5); len(got) != len(cells) {
		t.Errorf("limit == department count folded anyway: %d rows, want %d", len(got), len(cells))
	}
	if got := foldDepartmentTopicTail(cells, 0); len(got) != len(cells) {
		t.Errorf("limit 0 folded anyway: %d rows, want %d", len(got), len(cells))
	}

	got := foldDepartmentTopicTail(cells, 2)
	if total(got) != want {
		t.Errorf("prompts after fold = %d, want %d (the fold must conserve volume)", total(got), want)
	}

	kept := map[string]bool{}
	var otherPrompts, otherRows int64
	var otherCount int64
	for _, c := range got {
		if c.Other {
			otherRows++
			otherPrompts += c.Prompts
			otherCount = c.OtherCount
			if c.Department != "" {
				t.Errorf("Other row carries department %q, want empty", c.Department)
			}
			continue
		}
		kept[c.Department] = true
	}
	// Only the two highest-volume departments stay individual.
	if len(kept) != 2 || !kept["big"] || !kept["mid"] {
		t.Errorf("kept = %v, want exactly big and mid", kept)
	}
	if otherCount != 3 {
		t.Errorf("OtherCount = %d, want 3 folded departments", otherCount)
	}
	if otherPrompts != vol["small"]+vol["tiny"]+vol["micro"] {
		t.Errorf("Other prompts = %d, want %d", otherPrompts, vol["small"]+vol["tiny"]+vol["micro"])
	}
	// The tail collapses to one row per topic, not one per department.
	if otherRows != 2 {
		t.Errorf("Other rows = %d, want 2 (one per topic)", otherRows)
	}
}

// TestClassificationDepartmentTopicsLimit checks the cap end to end through the
// query, with the directory resolving two departments.
func TestClassificationDepartmentTopicsLimit(t *testing.T) {
	s, ctx := classificationFixture(t)
	s.SetDirectory(classDeptStub{})
	if err := s.SyncDirectory(ctx); err != nil {
		t.Fatalf("sync directory: %v", err)
	}
	from, to := classificationWindow()

	// limit 1 keeps only Engineering (4 prompts) and folds Legal & Compliance (1).
	got, err := s.QueryClassificationDepartmentTopics(ctx, from, to, 1, Filter{})
	if err != nil {
		t.Fatalf("department topics: %v", err)
	}
	var named, other int
	var prompts int64
	for _, c := range got {
		prompts += c.Prompts
		if c.Other {
			other++
			if c.OtherCount != 1 {
				t.Errorf("OtherCount = %d, want 1", c.OtherCount)
			}
		} else if c.Department != "Engineering" {
			t.Errorf("kept department %q, want Engineering", c.Department)
		} else {
			named++
		}
	}
	if named != 1 || other != 1 {
		t.Errorf("rows: named=%d other=%d, want 1/1", named, other)
	}
	if prompts != 5 {
		t.Errorf("prompts after cap = %d, want 5 (all prompts retained)", prompts)
	}
}

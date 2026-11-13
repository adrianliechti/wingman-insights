package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"insights/internal/store"
)

func TestCostEndpointsPreserveGroupedJSONAndCSV(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "costs.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	if _, err := s.DB().Exec(`INSERT INTO directory(alias,id,name,kind,department,location,username,active) VALUES
	 ('alice','alice','Alice','user','Eng','Zurich','alice',true),
	 ('alice@example.test','alice','Alice','user','Eng','Zurich','alice',true),
	 ('worker','app-canonical','Worker','application','','','',true)`); err != nil {
		t.Fatal(err)
	}
	var spans []store.SpanRow
	for i := 0; i < 6; i++ {
		spans = append(spans, store.SpanRow{ReceivedAt: at, Time: at, TraceID: "cost", SpanID: fmt.Sprint(i), UserID: []string{"alice", "unknown", ""}[i%3], UserEmail: []string{"", "alice@example.test", "unresolved@example.test"}[i%3], AppID: []string{"worker", "unknown-app"}[i%2], ProviderName: "synthetic", RequestModel: []string{"m1", "m2"}[i%2], InputTokens: int64(100 + i), OutputTokens: 20, CacheRead: 10, CacheCreation: 5, Reasoning: 3})
	}
	if err := s.InsertSpans(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	// Binary fractions make exact JSON comparison independent of SUM order.
	if _, err := s.DB().Exec(`UPDATE genai_spans SET input_cost=id*0.125,output_cost=id*0.25,cache_read_cost=id*0.0625,cache_creation_cost=id*0.03125,cost=id*0.5,cache_savings=id*0.0625,priced=(id%2=0)`); err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: s}
	for _, user := range []string{"", "alice", "missing"} {
		f := store.Filter{}
		if user != "" {
			f.User = []string{user}
		}
		base, err := s.QueryCostBreakdown(context.Background(), at.Add(-time.Hour), at.Add(time.Hour), f)
		if err != nil {
			t.Fatal(err)
		}
		for _, group := range []string{"", "invalid", "none", "user", "model", "app", "department", "location"} {
			t.Run(user+"/"+group, func(t *testing.T) {
				params := url.Values{"from": {at.Add(-time.Hour).Format(time.RFC3339)}, "to": {at.Add(time.Hour).Format(time.RFC3339)}, "group_by": {group}, "user": {user}}
				res := httptest.NewRecorder()
				h.costs(res, httptest.NewRequest("GET", "/api/genai/costs?"+params.Encode(), nil))
				if res.Code != 200 {
					t.Fatalf("JSON %d: %s", res.Code, res.Body.String())
				}
				if len(base) == 0 && strings.TrimSpace(res.Body.String()) != "[]" {
					t.Fatalf("empty JSON must remain [], got %s", res.Body.String())
				}
				var got []store.CostRow
				if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				want := append([]store.CostRow(nil), aggregateCostRows(base, group, "user")...)
				key := func(r store.CostRow) string {
					return strings.Join([]string{r.ID, r.AppID, r.ProviderName, r.RequestModel, r.Department, r.Location}, "\x00")
				}
				sort.Slice(got, func(i, j int) bool { return key(got[i]) < key(got[j]) })
				sort.Slice(want, func(i, j int) bool { return key(want[i]) < key(want[j]) })
				if len(got) != len(want) {
					t.Fatalf("JSON rows %d want %d", len(got), len(want))
				}
				for i := range want {
					if !reflect.DeepEqual(got[i], want[i]) {
						t.Errorf("JSON got %+v want %+v", got[i], want[i])
					}
				}
				res = httptest.NewRecorder()
				h.costReport(res, httptest.NewRequest("GET", "/api/genai/cost-report?"+params.Encode(), nil))
				if res.Code != 200 {
					t.Fatalf("CSV %d: %s", res.Code, res.Body.String())
				}
				if !strings.HasPrefix(res.Body.String(), "\xef\xbb\xbf") {
					t.Fatal("CSV lost UTF-8 BOM")
				}
				records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(res.Body.String(), "\xef\xbb\xbf"))).ReadAll()
				if err != nil {
					t.Fatal(err)
				}
				want = aggregateCostRows(base, group, "none")
				if len(records) != len(want)+2 {
					t.Fatalf("CSV records %d want %d", len(records), len(want)+2)
				}
				var total float64
				for i, r := range want {
					total += r.TotalCost
					expected := []string{r.ID, r.Name, r.Kind, r.Department, r.Location, r.Username, strconv.FormatBool(r.Former), r.AppID, r.ProviderName, r.RequestModel, fmtTokens(r.InputTokens), fmtTokens(r.OutputTokens), fmtTokens(r.CacheReadTokens), fmtTokens(r.CacheCreationTokens), fmtTokens(r.ReasoningTokens), fmtCost(r.InputCost), fmtCost(r.OutputCost), fmtCost(r.CacheReadCost), fmtCost(r.CacheCreationCost), fmtCost(r.TotalCost), strconv.FormatBool(r.Priced)}
					if !reflect.DeepEqual(records[i+1], expected) {
						t.Errorf("CSV row got %v want %v", records[i+1], expected)
					}
				}
				if records[len(records)-1][0] != "TOTAL" || records[len(records)-1][19] != fmtCost(total) {
					t.Fatalf("CSV total changed: %v", records[len(records)-1])
				}
			})
		}
	}
}

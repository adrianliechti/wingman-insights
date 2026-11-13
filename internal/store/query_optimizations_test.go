package store

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The reference queries below retain the pre-optimization algorithms: resolve
// every telemetry row, then COUNT(DISTINCT), with correlated directory EXISTS.
// Keeping that oracle separate catches interaction bugs between the rewrites.
func legacyAttributeClause(idCol, emailCol, col string, values []string, prefix bool) (string, []any) {
	if len(values) == 0 {
		return "", nil
	}
	pred := "lower(" + col + ") IN (" + lowerInPlaceholders(len(values)) + ")"
	var args []any
	for _, v := range values {
		args = append(args, strings.ToLower(v))
	}
	if prefix {
		var preds []string
		args = nil
		for _, v := range values {
			preds = append(preds, "lower("+col+") LIKE lower(?) ESCAPE '\\'")
			args = append(args, likeEscape(v)+"%")
		}
		pred = "(" + strings.Join(preds, " OR ") + ")"
	}
	return " AND EXISTS (SELECT 1 FROM directory WHERE " + pred + " AND (lower(" + idCol + ") = alias OR lower(" + emailCol + ") = alias))", args
}

func legacyFilterClause(f Filter, idCol, emailCol string) (string, []any) {
	var clause string
	var args []any
	add := func(c string, a []any) { clause += c; args = append(args, a...) }
	add(userClause(idCol, emailCol, f.User))
	add(legacyAttributeClause(idCol, emailCol, "department", f.Department, f.DeptPrefix))
	add(legacyAttributeClause(idCol, emailCol, "location", f.Location, false))
	add(appClause(f.App))
	add(providerClause(f.Provider))
	add(modelClause(f.Models))
	return clause, args
}

func legacyActiveUsers(s *Store, at time.Time, f Filter) (ActiveUsersRow, error) {
	clause, args := legacyFilterClause(f, "enduser_id", "enduser_email")
	r := dirResolve("genai_metrics", "enduser_id", "enduser_email")
	args = append([]any{at.Add(-24 * time.Hour), at.Add(-7 * 24 * time.Hour), at.Add(-30 * 24 * time.Hour), at}, args...)
	var out ActiveUsersRow
	err := s.db.QueryRow(`SELECT COUNT(DISTINCT CASE WHEN time>=? THEN `+r.ID+` END),
	 COUNT(DISTINCT CASE WHEN time>=? THEN `+r.ID+` END),COUNT(DISTINCT `+r.ID+`)
	 FROM genai_metrics`+r.Join+` WHERE enduser_id IS NOT NULL AND enduser_id<>'' AND time>=? AND time<=?`+clause, args...).Scan(&out.DAU, &out.WAU, &out.MAU)
	return out, err
}

func legacyUserStatsCount(s *Store, from, to time.Time, f Filter) (int64, error) {
	clause, args := legacyFilterClause(f, "user_id", "user_email")
	r := dirResolve("genai_spans", "user_id", "user_email")
	args = append([]any{from, to}, args...)
	var count int64
	err := s.db.QueryRow(`SELECT COUNT(DISTINCT `+r.ID+`) FROM genai_spans`+r.Join+`
	 WHERE (input_tokens>0 OR output_tokens>0) AND user_id IS NOT NULL AND user_id<>'' AND time>=? AND time<=?`+clause, args...).Scan(&count)
	return count, err
}

func optimizationFixture(t *testing.T) (*Store, time.Time) {
	t.Helper()
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "test.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	t.Setenv("INSIGHTS_DB_THREADS", "2")
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.db.Exec(`INSERT INTO directory(alias,id,name,kind,department,location,username,active) VALUES
	 ('alice-raw','alice','Alice','user','A_1','Zurich','alice',true),
	 ('alice@example.test','alice','Alice','user','A_1','Zurich','alice',true),
	 ('alice-old','alice','Alice','user','A_1','Zurich','alice',false),
	 ('bob-raw','bob','Bob','user','Sales','Vaduz','bob',true),
	 ('bob@example.test','bob','Bob','user','Sales','Vaduz','bob',true),
	 ('child','child','Child','user','A_1.child','Zurich','child',true),
	 ('percent','percent','Percent','user','A%literal','Vienna','percent',true),
	 ('backslash','backslash','Backslash','user','A\unit','Zurich','backslash',true),
	 ('unassigned','unassigned','Unassigned','user',NULL,NULL,NULL,true),
	 ('empty-principal','','Empty principal','user','A_1','Zurich','',true),
	 ('app-alias','app-canonical','Worker','application','','','',true),
	 ('app-canonical','app-canonical','Worker','application','','','',true)`); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	type event struct {
		age                  time.Duration
		id, email            any
		app, provider, model any
	}
	events := []event{
		{0, "alice-raw", "alice@example.test", "app-alias", "openai", "m1"},
		{time.Hour, "ALICE-RAW", "", "app-canonical", "openai", "m1"},
		{2 * time.Hour, "alice-old", "", "app-alias", "openai", "m2"},
		{3 * time.Hour, "unknown-id", "ALICE@example.test", "unknown-app", "anthropic", "m2"},
		{4 * time.Hour, "bob-raw", "alice@example.test", "app-alias", "anthropic", "m1"},
		{5 * time.Hour, "child", "", "unknown-app", "openai", "m1"},
		{6 * time.Hour, "percent", "", "app-alias", "openai", "m2"},
		{7 * time.Hour, "backslash", "", "unknown-app", "anthropic", "m1"},
		{8 * time.Hour, "unassigned", nil, nil, nil, nil},
		{9 * time.Hour, "empty-principal", "", "app-alias", "openai", "m1"},
		{10 * time.Hour, "ghost-a", "shared@example.test", "unknown-app", "openai", "m1"},
		{11 * time.Hour, "ghost-b", "shared@example.test", "unknown-app", "openai", "m1"},
		{12 * time.Hour, "app-alias", "", "app-alias", "openai", "m1"},
		{13 * time.Hour, "", "alice@example.test", "app-alias", "openai", "m1"},
		{14 * time.Hour, nil, "bob@example.test", "unknown-app", "openai", "m1"},
		{15 * time.Hour, nil, nil, nil, nil, nil},
		{24 * time.Hour, "day-boundary", "", "unknown-app", "openai", "m1"},
		{24*time.Hour + time.Microsecond, "before-day", "", "unknown-app", "openai", "m1"},
		{7 * 24 * time.Hour, "week-boundary", "", "unknown-app", "openai", "m1"},
		{7*24*time.Hour + time.Microsecond, "before-week", "", "unknown-app", "openai", "m1"},
		{30 * 24 * time.Hour, "month-boundary", "", "unknown-app", "openai", "m1"},
		{30*24*time.Hour + time.Microsecond, "too-old", "", "unknown-app", "openai", "m1"},
		{-time.Microsecond, "future", "", "unknown-app", "openai", "m1"},
		{31 * 24 * time.Hour, "older-with-tokens", "", "unknown-app", "openai", "m1"},
	}
	for i, e := range events {
		for repeat := 0; repeat < 3; repeat++ {
			ts := at.Add(-e.age)
			var input, output, cache, priced any = 100 + i, 20 + i, 30, true
			if i%6 == 0 {
				input = nil
			}
			if i%5 == 0 {
				cache = nil
			}
			if i%7 == 0 {
				input, output = 0, 0
			}
			if i%3 == 0 {
				priced = nil
			} else if i%3 == 1 {
				priced = false
			}
			if _, err := s.db.Exec(`INSERT INTO genai_spans(received_at,time,trace_id,span_id,user_id,user_email,app_id,provider_name,request_model,
			 input_tokens,output_tokens,cache_read_tokens,cache_creation_tokens,reasoning_tokens,
			 input_cost,output_cost,cache_read_cost,cache_creation_cost,cost,cache_savings,priced)
			 VALUES(?,?,'test',?,?,?,?,?,?, ?,?,?,10,5, 0.01,0.02,0.001,0.002,?,0.003,?)`,
				ts, ts, fmt.Sprint(i, "-", repeat), e.id, e.email, e.app, e.provider, e.model, input, output, cache, float64(i+repeat+1)/100, priced); err != nil {
				t.Fatal(err)
			}
			// Active-users intentionally counts all metric names, not only usage.
			if _, err := s.db.Exec(`INSERT INTO genai_metrics(received_at,time,metric_name,enduser_id,enduser_email,app_id,provider_name,request_model)
			 VALUES(?,?,?,?,?,?,?,?)`, ts, ts, []string{"gen_ai.client.token.usage", "gen_ai.client.operation.duration", "custom.metric"}[repeat], e.id, e.email, e.app, e.provider, e.model); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s, at
}

func optimizationFilters() []Filter {
	return []Filter{
		{}, {User: []string{"alice"}}, {User: []string{"ALICE-RAW", "shared@example.test"}},
		{User: []string{"does-not-exist"}}, {Department: []string{"A_1"}},
		{Department: []string{"A_1", "Sales"}}, {Department: []string{"a_"}, DeptPrefix: true},
		{Department: []string{"A%"}, DeptPrefix: true}, {Department: []string{"A\\"}, DeptPrefix: true},
		{Department: []string{"A_1", "Sales"}, DeptPrefix: true}, {Department: []string{"missing"}},
		{Location: []string{"ZURICH", "Vaduz"}}, {App: []string{"APP-CANONICAL"}},
		{Provider: []string{"openai"}}, {Models: []string{"m1"}},
		{Department: []string{"Sales"}, Location: []string{"Zurich"}}, // ID and email may match different records.
		{User: []string{"alice"}, App: []string{"app-canonical"}, Department: []string{"A_"}, DeptPrefix: true, Location: []string{"Zurich"}, Provider: []string{"openai"}, Models: []string{"m1"}},
	}
}

func TestCountAndFilterOptimizationsPreserveResults(t *testing.T) {
	s, at := optimizationFixture(t)
	ctx := context.Background()
	from := at.Add(-30 * 24 * time.Hour)
	for _, state := range []string{"original-directory", "remapped-directory", "empty-directory", "empty-telemetry"} {
		switch state {
		case "remapped-directory":
			if _, err := s.db.Exec(`UPDATE directory SET id='bob',department='Sales',location='Vaduz' WHERE id='alice'`); err != nil {
				t.Fatal(err)
			}
		case "empty-directory":
			if _, err := s.db.Exec("DELETE FROM directory"); err != nil {
				t.Fatal(err)
			}
		case "empty-telemetry":
			if _, err := s.db.Exec("DELETE FROM genai_spans; DELETE FROM genai_metrics"); err != nil {
				t.Fatal(err)
			}
		}
		t.Run(state, func(t *testing.T) {
			for i, f := range optimizationFilters() {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					want, err := legacyActiveUsers(s, at, f)
					if err != nil {
						t.Fatal(err)
					}
					got, err := s.QueryActiveUsers(ctx, at, f)
					if err != nil {
						t.Fatal(err)
					}
					if *got != want {
						t.Fatalf("active users: got %+v want %+v; filter %+v", *got, want, f)
					}
					wantCount, err := legacyUserStatsCount(s, from, at, f)
					if err != nil {
						t.Fatal(err)
					}
					gotCount, err := s.QueryUserStatsCount(ctx, from, at, f)
					if err != nil {
						t.Fatal(err)
					}
					if gotCount != wantCount {
						t.Fatalf("users: got %d want %d; filter %+v", gotCount, wantCount, f)
					}
					for _, table := range []string{"genai_spans", "genai_metrics"} {
						id, email := "user_id", "user_email"
						if table == "genai_metrics" {
							id, email = "enduser_id", "enduser_email"
						}
						oldClause, oldArgs := legacyFilterClause(f, id, email)
						newClause, newArgs := f.clause(id, email)
						read := func(clause string, args []any) []int64 {
							rows, err := s.db.Query("SELECT id FROM "+table+" WHERE true"+clause+" ORDER BY id", args...)
							if err != nil {
								t.Fatal(err)
							}
							defer rows.Close()
							var out []int64
							for rows.Next() {
								var n int64
								if err := rows.Scan(&n); err != nil {
									t.Fatal(err)
								}
								out = append(out, n)
							}
							if err := rows.Err(); err != nil {
								t.Fatal(err)
							}
							return out
						}
						if a, b := read(oldClause, oldArgs), read(newClause, newArgs); !reflect.DeepEqual(a, b) {
							t.Fatalf("%s filter changed row membership: %v vs %v", table, a, b)
						}
					}
				})
			}
		})
	}
}

func TestDirectCostSummariesMatchBreakdownAggregation(t *testing.T) {
	s, at := optimizationFixture(t)
	ctx := context.Background()
	from := at.Add(-30 * 24 * time.Hour)
	for _, state := range []string{"original-directory", "remapped-directory", "empty-directory", "empty-telemetry"} {
		switch state {
		case "remapped-directory":
			if _, err := s.db.Exec(`UPDATE directory SET id='bob',department='Sales',location='Vaduz' WHERE id='alice'`); err != nil {
				t.Fatal(err)
			}
		case "empty-directory":
			if _, err := s.db.Exec("DELETE FROM directory"); err != nil {
				t.Fatal(err)
			}
		case "empty-telemetry":
			if _, err := s.db.Exec("DELETE FROM genai_spans"); err != nil {
				t.Fatal(err)
			}
		}
		t.Run(state, func(t *testing.T) {
			for i, f := range optimizationFilters() {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					breakdown, err := s.QueryCostBreakdown(ctx, from, at, f)
					if err != nil {
						t.Fatal(err)
					}
					for group, aggregate := range map[string]func([]CostRow) []CostRow{"model": AggregateCostsByModel, "department": AggregateCostsByDepartment, "location": AggregateCostsByLocation} {
						got, err := s.QueryCostSummary(ctx, from, at, group, f)
						if err != nil {
							t.Fatal(err)
						}
						assertCostRowsEqual(t, got, aggregate(breakdown))
					}
				})
			}
		})
	}
}

func assertCostRowsEqual(t *testing.T, got, want []CostRow) {
	t.Helper()
	key := func(r CostRow) string {
		return strings.Join([]string{r.ID, r.AppID, r.ProviderName, r.RequestModel, r.Department, r.Location}, "\x00")
	}
	if len(got) != len(want) {
		t.Fatalf("cost rows: got %d want %d", len(got), len(want))
	}
	byKey := map[string]CostRow{}
	for _, r := range want {
		byKey[key(r)] = r
	}
	for _, r := range got {
		w, ok := byKey[key(r)]
		if !ok {
			t.Fatalf("unexpected cost row %+v", r)
		}
		delete(byKey, key(r))
		gv, wv := reflect.ValueOf(r), reflect.ValueOf(w)
		for i := 0; i < gv.NumField(); i++ {
			g, w := gv.Field(i), wv.Field(i)
			if g.Kind() == reflect.Float64 {
				if math.Abs(g.Float()-w.Float()) > 1e-10*math.Max(1, math.Abs(w.Float())) {
					t.Errorf("%s: got %g want %g", gv.Type().Field(i).Name, g.Float(), w.Float())
				}
			} else if !reflect.DeepEqual(g.Interface(), w.Interface()) {
				t.Errorf("%s: got %v want %v", gv.Type().Field(i).Name, g.Interface(), w.Interface())
			}
		}
	}
}

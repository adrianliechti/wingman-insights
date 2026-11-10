package mcpserver

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"insights/internal/store"
)

func TestUsagePagination(t *testing.T) {
	s := fixture(t)
	ts := time.Now().UTC().Add(-time.Hour)
	spans := make([]store.SpanRow, 105)
	for i := range spans {
		spans[i] = store.SpanRow{Time: ts, ReceivedAt: ts, UserID: fmt.Sprintf("page-%03d", i), AppID: "pagination", InputTokens: 1}
	}
	if err := s.InsertSpans(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	client, _ := connect(t, s, "admin", "admins")
	args := map[string]any{"group_by": "user", "limit": 100, "applications": []string{"pagination"}, "from": ts.Add(-time.Hour).Format(time.RFC3339), "to": ts.Add(time.Hour).Format(time.RFC3339)}
	first := call[Response[UsageData]](t, client, "get_usage", args)
	if len(first.Data.Groups) != 100 || first.Data.TotalGroups != 105 || !first.Data.Truncated || first.Data.NextOffset == nil || *first.Data.NextOffset != 100 || first.Query.Limit != 100 {
		t.Fatalf("first page: %+v", first)
	}
	for i, g := range first.Data.Groups {
		if g.ID != fmt.Sprintf("page-%03d", i) {
			t.Fatalf("unstable tie order: %s", g.ID)
		}
	}
	args["offset"] = *first.Data.NextOffset
	last := call[Response[UsageData]](t, client, "get_usage", args)
	if len(last.Data.Groups) != 5 || last.Data.TotalGroups != 105 || last.Data.Truncated || last.Data.NextOffset != nil || last.Query.Offset != 100 || !reflect.DeepEqual(first.Data.Totals, last.Data.Totals) {
		t.Fatalf("last page: %+v", last)
	}
	for i, g := range last.Data.Groups {
		if g.ID != fmt.Sprintf("page-%03d", i+100) {
			t.Fatalf("overlapping page: %s", g.ID)
		}
	}
	args["offset"] = 1000000
	empty := call[Response[UsageData]](t, client, "get_usage", args)
	if len(empty.Data.Groups) != 0 || empty.Data.TotalGroups != 105 || empty.Data.Truncated || empty.Data.NextOffset != nil || len(empty.Warnings) != len(first.Warnings) || !reflect.DeepEqual(empty.Data.Totals, first.Data.Totals) {
		t.Fatalf("beyond end: %+v", empty)
	}
	for _, group := range []string{"department", "model", "application", "location"} {
		args["group_by"], args["limit"], args["offset"] = group, 1, 0
		page := call[Response[UsageData]](t, client, "get_usage", args)
		args["offset"] = 1
		end := call[Response[UsageData]](t, client, "get_usage", args)
		if len(page.Data.Groups) != 1 || len(end.Data.Groups) != 0 || !reflect.DeepEqual(page.Data.Totals, end.Data.Totals) {
			t.Fatalf("%s pagination: %+v %+v", group, page, end)
		}
	}
}

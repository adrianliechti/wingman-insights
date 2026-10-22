package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"insights/internal/store"
)

func TestDepartmentTopicsCanReturnAll700Departments(t *testing.T) {
	t.Setenv("INSIGHTS_DB_PATH", filepath.Join(t.TempDir(), "insights.db"))
	t.Setenv("INSIGHTS_DB_MEMORY_LIMIT", "512MB")
	s, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO directory(alias,id,name,kind,department) VALUES (?,?,?,'user',?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	now := time.Now().UTC().Add(-time.Hour)
	var metrics []store.GenAIMetricRow
	for i := range 700 {
		user := fmt.Sprintf("user-%03d", i)
		if _, err := stmt.Exec(user, user, user, fmt.Sprintf("Team %03d", i)); err != nil {
			t.Fatal(err)
		}
		metrics = append(metrics, store.GenAIMetricRow{
			ReceivedAt: now, Time: now, MetricName: "wingman.classification.score", EndUserID: user, Count: 1, Sum: 0.9,
			Attributes: map[string]string{"wingman.classification.kind": "category", "wingman.classification.id": "research", "wingman.classification.matched": "true"},
		})
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertGenAIMetrics(context.Background(), metrics); err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: s}
	res := httptest.NewRecorder()
	h.classificationDepartmentTopics(res, httptest.NewRequest("GET", "/", nil))
	if res.Code != 200 {
		t.Fatalf("status %d: %s", res.Code, res.Body.String())
	}
	var cells []store.ClassificationDepartmentTopicCell
	if err := json.Unmarshal(res.Body.Bytes(), &cells); err != nil {
		t.Fatal(err)
	}
	if len(cells) != 700 {
		t.Fatalf("cells = %d, want 700", len(cells))
	}
	var total int64
	for _, cell := range cells {
		total += cell.Prompts
	}
	if total != 700 {
		t.Fatalf("prompts = %d, want 700", total)
	}
}

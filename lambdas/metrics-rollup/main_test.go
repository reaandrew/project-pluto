package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Date(2026, 5, 16, 9, 0, 0, 0, time.UTC) }

func TestRollup_WritesFunnelAndCost(t *testing.T) {
	counts := map[string]int{"new": 12, "qualified": 5, "converted": 2}
	var got MetricRow
	d := runDeps{
		CountByStatus: func(_ context.Context, s string) (int, error) { return counts[s], nil },
		CostForDate: func(_ context.Context, date string) (map[string]float64, error) {
			if date != "2026-05-16" {
				t.Errorf("date drift: %s", date)
			}
			return map[string]float64{"audit": 1.50, "outreach": 0.25}, nil
		},
		PutMetric: func(_ context.Context, m MetricRow) error { got = m; return nil },
		Now:       fixedNow,
	}
	if err := rollup(context.Background(), d); err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if got.PK != "METRIC#2026-05-16" || got.SK != "ROLLUP" || got.Type != "Metric" {
		t.Errorf("key drift: %+v", got)
	}
	if len(got.Funnel) != len(FunnelStatuses) || got.Funnel["new"] != 12 || got.Funnel["converted"] != 2 {
		t.Errorf("funnel drift: %+v", got.Funnel)
	}
	// Statuses with no rows must still appear as 0 (the funnel is fixed-shape).
	if _, ok := got.Funnel["emailed"]; !ok || got.Funnel["emailed"] != 0 {
		t.Errorf("missing zero-filled status: %+v", got.Funnel)
	}
	if got.TotalCost != 1.75 || got.CostByStage["audit"] != 1.50 {
		t.Errorf("cost drift: total=%v byStage=%+v", got.TotalCost, got.CostByStage)
	}
	if got.ExpiresAt == 0 || got.GeneratedAt != "2026-05-16T09:00:00Z" {
		t.Errorf("ttl/timestamp drift: %+v", got)
	}
}

func TestRollup_CountErrorAborts(t *testing.T) {
	d := runDeps{
		CountByStatus: func(context.Context, string) (int, error) { return 0, errors.New("ddb down") },
		CostForDate:   func(context.Context, string) (map[string]float64, error) { return nil, nil },
		PutMetric:     func(context.Context, MetricRow) error { t.Fatal("must not write on count error"); return nil },
		Now:           fixedNow,
	}
	if err := rollup(context.Background(), d); err == nil {
		t.Fatal("a count failure must abort the rollup")
	}
}

func TestRollup_NoCostRecords_ZeroTotal(t *testing.T) {
	var got MetricRow
	d := runDeps{
		CountByStatus: func(context.Context, string) (int, error) { return 0, nil },
		CostForDate:   func(context.Context, string) (map[string]float64, error) { return map[string]float64{}, nil },
		PutMetric:     func(_ context.Context, m MetricRow) error { got = m; return nil },
		Now:           fixedNow,
	}
	if err := rollup(context.Background(), d); err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if got.TotalCost != 0 || len(got.CostByStage) != 0 {
		t.Errorf("expected zero cost, got %+v", got)
	}
}

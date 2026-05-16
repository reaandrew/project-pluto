package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Date(2026, 5, 16, 9, 0, 0, 0, time.UTC) }

func baseDeps(put *MetricRow) runDeps {
	return runDeps{
		StatusBreakdown: func(_ context.Context, s string) (int, map[string]int, error) {
			switch s {
			case "emailed":
				return 10, map[string]int{"accountants": 7, "default": 3}, nil
			case "responded":
				return 4, map[string]int{"accountants": 3, "default": 1}, nil
			case "converted":
				return 1, map[string]int{"accountants": 1}, nil
			}
			return 0, nil, nil
		},
		ProfileVersions: func(_ context.Context, v string) (int, int, error) {
			if v == "accountants" {
				return 4, 2, nil
			}
			return 1, 1, nil
		},
		CostForDate: func(_ context.Context, date string) (map[string]float64, error) {
			return map[string]float64{"audit": 1.5, "outreach": 0.25}, nil
		},
		PutMetric: func(_ context.Context, m MetricRow) error { *put = m; return nil },
		Now:       fixedNow,
	}
}

func TestRollup_WritesRangeQueryableKeyAndPerVertical(t *testing.T) {
	var got MetricRow
	if err := rollup(context.Background(), baseDeps(&got)); err != nil {
		t.Fatalf("rollup: %v", err)
	}
	// Single-partition, range-queryable key.
	if got.PK != "METRIC" || got.SK != "DATE#2026-05-16" || got.Type != "Metric" {
		t.Errorf("key drift: %+v", got)
	}
	if got.Funnel["emailed"] != 10 || got.Funnel["responded"] != 4 || got.Funnel["converted"] != 1 {
		t.Errorf("global funnel drift: %+v", got.Funnel)
	}
	// Seeded verticals always present, even with no businesses.
	if got.PerVertical["default"] == nil || got.PerVertical["accountants"] == nil {
		t.Fatalf("seeded verticals missing: %+v", got.PerVertical)
	}
	acc := got.PerVertical["accountants"]
	if acc.Funnel["emailed"] != 7 || acc.Funnel["responded"] != 3 || acc.Funnel["converted"] != 1 {
		t.Errorf("accountants funnel drift: %+v", acc.Funnel)
	}
	// Profile versions tag the day so the version-split UI can correlate.
	if acc.StyleVersion != 4 || acc.ToneVersion != 2 {
		t.Errorf("accountants versions drift: %+v", acc)
	}
	if got.PerVertical["default"].StyleVersion != 1 {
		t.Errorf("default versions drift: %+v", got.PerVertical["default"])
	}
	if got.TotalCost != 1.75 || got.CostByStage["audit"] != 1.5 {
		t.Errorf("cost drift: %v %+v", got.TotalCost, got.CostByStage)
	}
	if got.ExpiresAt == 0 || got.GeneratedAt != "2026-05-16T09:00:00Z" {
		t.Errorf("ttl/timestamp drift: %+v", got)
	}
}

func TestRollup_StatusBreakdownErrorAborts(t *testing.T) {
	d := baseDeps(&MetricRow{})
	d.StatusBreakdown = func(context.Context, string) (int, map[string]int, error) {
		return 0, nil, errors.New("ddb down")
	}
	d.PutMetric = func(context.Context, MetricRow) error { t.Fatal("must not write on error"); return nil }
	if err := rollup(context.Background(), d); err == nil {
		t.Fatal("a breakdown failure must abort the rollup")
	}
}

func TestRollup_ProfileVersionErrorAborts(t *testing.T) {
	d := baseDeps(&MetricRow{})
	d.ProfileVersions = func(context.Context, string) (int, int, error) {
		return 0, 0, errors.New("style.Get boom")
	}
	d.PutMetric = func(context.Context, MetricRow) error { t.Fatal("must not write"); return nil }
	if err := rollup(context.Background(), d); err == nil {
		t.Fatal("a profile-version failure must abort")
	}
}

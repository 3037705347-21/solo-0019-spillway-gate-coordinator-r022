package domain

import (
	"testing"
	"time"
)

func TestValidateAdjustmentWindow(t *testing.T) {
	cases := []struct {
		name    string
		seconds int64
		limit   float64
		wantErr bool
	}{
		{"disabled by zero values", 0, 0, false},
		{"enabled", 3600, 2.5, false},
		{"negative seconds", -1, 2.5, true},
		{"negative limit", 3600, -1, true},
		{"seconds without limit rejected", 3600, 0, true},
		{"limit without seconds rejected", 0, 2.5, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAdjustmentWindow(tc.seconds, tc.limit)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestAdjustmentMagnitudeIsDirectionAgnostic(t *testing.T) {
	if got := AdjustmentMagnitude(1.0, 3.5); got != 2.5 {
		t.Fatalf("opening adjustment: expected 2.5, got %v", got)
	}
	if got := AdjustmentMagnitude(3.5, 1.0); got != 2.5 {
		t.Fatalf("closing adjustment: expected 2.5, got %v", got)
	}
}

func TestEvaluateAdjustmentWindowCountsOnlyEffectiveFactsInsideInterval(t *testing.T) {
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	config := AdjustmentWindowConfig{WindowSeconds: 60, MaxAdjustment: 5}

	facts := []AdjustmentFact{
		{CommandID: "old", Adjustment: 10, ExecutedAt: now.Add(-61 * time.Second)},
		{CommandID: "edge", Adjustment: 1, ExecutedAt: now.Add(-60 * time.Second)},
		{CommandID: "open", Status: CommandCompleted, Adjustment: 1.5, ExecutedAt: now.Add(-30 * time.Second)},
		{CommandID: "close", Status: CommandAborted, Adjustment: 0.5, ExecutedAt: now.Add(-10 * time.Second)},
	}

	snapshot := EvaluateAdjustmentWindow(config, facts, now)
	if !snapshot.Enabled {
		t.Fatal("window should be enabled")
	}
	// Half-open interval (start, now]: a fact exactly one window old is
	// released; older facts must not poison a fresh window.
	if got := snapshot.Used; got != 2.0 {
		t.Fatalf("used: expected 2 (boundary facts released), got %v", got)
	}
	if got := snapshot.Available; got != 3.0 {
		t.Fatalf("available: expected 3, got %v", got)
	}
	if len(snapshot.Facts) != 2 || snapshot.Facts[0].CommandID != "open" || snapshot.Facts[1].CommandID != "close" {
		t.Fatalf("facts should be the two in-window facts in execution order, got %+v", snapshot.Facts)
	}
	if !snapshot.WindowStart.Equal(now.Add(-60*time.Second)) || !snapshot.WindowEnd.Equal(now) {
		t.Fatalf("window bounds mismatch: %v .. %v", snapshot.WindowStart, snapshot.WindowEnd)
	}
}

func TestEvaluateAdjustmentWindowReleasesBudgetAsFactsAgeOut(t *testing.T) {
	base := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	config := AdjustmentWindowConfig{WindowSeconds: 60, MaxAdjustment: 2}
	facts := []AdjustmentFact{
		{CommandID: "a", Adjustment: 1.2, ExecutedAt: base},
		{CommandID: "b", Adjustment: 0.8, ExecutedAt: base.Add(5 * time.Second)},
	}

	full := EvaluateAdjustmentWindow(config, facts, base.Add(5*time.Second))
	if full.Used != 2.0 || full.Available != 0 {
		t.Fatalf("expected exhausted window, got used=%v available=%v", full.Used, full.Available)
	}

	// At 61s the first fact (base) is exactly one window old and is
	// released; the second is still counted.
	partial := EvaluateAdjustmentWindow(config, facts, base.Add(61*time.Second))
	if partial.Used != 0.8 || partial.Available != 1.2 || len(partial.Facts) != 1 {
		t.Fatalf("expected partially released window, got used=%v available=%v facts=%d",
			partial.Used, partial.Available, len(partial.Facts))
	}

	released := EvaluateAdjustmentWindow(config, facts, base.Add(66*time.Second))
	if released.Used != 0 || released.Available != 2.0 {
		t.Fatalf("expected fully released window, got used=%v available=%v", released.Used, released.Available)
	}
	if len(released.Facts) != 0 {
		t.Fatalf("aged-out history must not be listed, got %+v", released.Facts)
	}
}

func TestCheckAdmissionBoundaryAndOverLimit(t *testing.T) {
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	config := AdjustmentWindowConfig{WindowSeconds: 60, MaxAdjustment: 2}
	snapshot := EvaluateAdjustmentWindow(config, []AdjustmentFact{
		{CommandID: "a", Adjustment: 1.5, ExecutedAt: now},
	}, now)

	if err := snapshot.CheckAdmission(0.5); err != nil {
		t.Fatalf("cumulative exactly at the limit must be legal, got %v", err)
	}
	if err := snapshot.CheckAdmission(0.5 + 1e-10); err != nil {
		t.Fatalf("floating point noise at the boundary must not reject, got %v", err)
	}
	err := snapshot.CheckAdmission(0.6)
	if err == nil {
		t.Fatal("adjustment beyond available budget must be rejected")
	}
	if ErrorCodeOf(err) != "adjustment_window_exceeded" || ErrorKindOf(err) != KindConflict {
		t.Fatalf("expected stable conflict code adjustment_window_exceeded, got %v", err)
	}
}

func TestDisabledWindowAdmitsEverything(t *testing.T) {
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	snapshot := EvaluateAdjustmentWindow(AdjustmentWindowConfig{}, nil, now)
	if snapshot.Enabled {
		t.Fatal("zero config must disable the window")
	}
	if err := snapshot.CheckAdmission(999); err != nil {
		t.Fatalf("disabled window must not reject, got %v", err)
	}
}

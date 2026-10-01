package domain

import (
	"testing"
	"time"
)

func TestAdjustmentWindowAdmitsExactBoundaryAndRejectsBeyondIt(t *testing.T) {
	if err := CheckAdjustmentWindow(0.75, 0.25, 1.0); err != nil {
		t.Fatalf("exact boundary must be admitted, got %v", err)
	}
	if err := CheckAdjustmentWindow(0.75, 0.25+1e-8, 1.0); err == nil {
		t.Fatalf("strictly over the limit must be rejected")
	}
	err := CheckAdjustmentWindow(1.0, 0.1, 1.0)
	if err == nil {
		t.Fatalf("full window must reject further travel")
	}
	if ErrorCodeOf(err) != "adjustment_window_exceeded" {
		t.Fatalf("expected adjustment_window_exceeded, got %q", ErrorCodeOf(err))
	}
	if ErrorKindOf(err) != KindConflict {
		t.Fatalf("expected conflict kind, got %q", ErrorKindOf(err))
	}
}

func TestAdjustmentWindowFloatingPointBoundary(t *testing.T) {
	// 0.1 accumulated three times must still compare equal to 0.3, not be
	// rejected because of binary representation drift.
	if err := CheckAdjustmentWindow(0.1+0.1+0.1, 0.0, 0.3); err != nil {
		t.Fatalf("representation drift must not reject the limit: %v", err)
	}
}

func TestPartitionAdjustmentFacts(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	window := time.Minute
	facts := []AdjustmentFact{
		{CommandID: "aged-out", Amount: 1, EffectiveAt: now.Add(-2 * time.Minute)},
		{CommandID: "edge", Amount: 2, EffectiveAt: now.Add(-window)},
		{CommandID: "fresh", Amount: 4, EffectiveAt: now.Add(-time.Second)},
	}
	effective, released := PartitionAdjustmentFacts(facts, window, now)
	if len(effective) != 1 || effective[0].CommandID != "fresh" {
		t.Fatalf("expected only the fresh fact inside the window, got %#v", effective)
	}
	if len(released) != 2 {
		t.Fatalf("expected the aged-out fact and the exact-edge fact released, got %#v", released)
	}
	if used := SumAdjustmentAmounts(effective); used != 4 {
		t.Fatalf("expected used 4, got %v", used)
	}
}

func TestNewAdjustmentFactMeasuresAbsoluteTravel(t *testing.T) {
	now := time.Now()
	up := NewAdjustmentFact("g", "c1", 1.0, 1.5, AdjustmentCompleted, now)
	if up.Amount != 0.5 {
		t.Fatalf("upward travel amount: expected 0.5, got %v", up.Amount)
	}
	down := NewAdjustmentFact("g", "c2", 1.5, 0.5, AdjustmentCompleted, now)
	if down.Amount != 1.0 {
		t.Fatalf("downward travel amount: expected 1.0, got %v", down.Amount)
	}
	if up.Outcome != AdjustmentCompleted {
		t.Fatalf("expected completed outcome")
	}
	if !up.EffectiveAt.Equal(now.UTC()) {
		t.Fatalf("effective time must be normalized to UTC")
	}
}

func TestNewGateWindowConfiguration(t *testing.T) {
	now := time.Now()

	gate, err := NewGate("gate-window-1", "name", 5, 1, 60, 1.5, now)
	if err != nil {
		t.Fatalf("valid window configuration rejected: %v", err)
	}
	if !gate.AdjustmentWindowEnabled() || gate.AdjustmentWindow() != time.Minute {
		t.Fatalf("window must be enabled for one minute")
	}
	if gate.AdjustmentWindowLimit != 1.5 {
		t.Fatalf("unexpected limit %v", gate.AdjustmentWindowLimit)
	}

	plain, err := NewGate("gate-window-2", "name", 5, 1, 0, 0, now)
	if err != nil {
		t.Fatalf("windowless configuration rejected: %v", err)
	}
	if plain.AdjustmentWindowEnabled() {
		t.Fatalf("gate without settings must have the window disabled")
	}

	if _, err := NewGate("gate-window-3", "name", 5, 1, 60, 0, now); err == nil {
		t.Fatalf("seconds without limit must be rejected")
	} else if ErrorCodeOf(err) != "invalid_adjustment_window_limit" {
		t.Fatalf("expected invalid_adjustment_window_limit, got %q", ErrorCodeOf(err))
	}
	if _, err := NewGate("gate-window-4", "name", 5, 1, 0, 1.0, now); err == nil {
		t.Fatalf("limit without seconds must be rejected")
	} else if ErrorCodeOf(err) != "invalid_adjustment_window_seconds" {
		t.Fatalf("expected invalid_adjustment_window_seconds, got %q", ErrorCodeOf(err))
	}
	if _, err := NewGate("gate-window-5", "name", 5, 1, -3, 0, now); err == nil {
		t.Fatalf("negative window seconds must be rejected")
	}
}

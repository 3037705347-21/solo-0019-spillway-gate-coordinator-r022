package domain

import (
	"math"
	"sort"
	"time"
)

// adjustmentEpsilon keeps boundary comparisons explainable: a cumulative
// adjustment exactly equal to the safety limit stays legal, only values
// strictly beyond it (accounting for floating point noise) are rejected.
const adjustmentEpsilon = 1e-9

// AdjustmentWindowConfig is the per-gate duty-cycle safety limit. The zero
// value means no window is enforced: a gate is limited per command only.
type AdjustmentWindowConfig struct {
	WindowSeconds int64
	MaxAdjustment float64
}

func (c AdjustmentWindowConfig) Enabled() bool {
	return c.WindowSeconds > 0 && c.MaxAdjustment > 0
}

func (c AdjustmentWindowConfig) Duration() time.Duration {
	return time.Duration(c.WindowSeconds) * time.Second
}

// ValidateAdjustmentWindow enforces that the window limit is either switched
// off entirely or configured as a positive duration together with a positive
// limit; a half-configured window is rejected at gate registration.
func ValidateAdjustmentWindow(windowSeconds int64, maxAdjustment float64) error {
	if windowSeconds < 0 {
		return Invalid("invalid_adjustment_window", "adjustment window seconds must be zero (disabled) or positive")
	}
	if maxAdjustment < 0 {
		return Invalid("invalid_adjustment_window", "adjustment window limit must be zero (disabled) or positive")
	}
	if (windowSeconds > 0) != (maxAdjustment > 0) {
		return Invalid(
			"invalid_adjustment_window",
			"adjustment window requires both window_seconds and a positive adjustment limit",
		)
	}
	return nil
}

// AdjustmentMagnitude is the structural travel a single command imposes on
// the gate; opening and closing movements both consume the same budget.
func AdjustmentMagnitude(fromAperture, toAperture float64) float64 {
	return math.Abs(toAperture - fromAperture)
}

// AdjustmentFact is an effective execution fact: only commands that have
// actually moved the gate (completed or aborted after settle) consume duty
// window budget. Pending, approved-but-unexecuted and rejected commands are
// not facts and never reserve window capacity.
type AdjustmentFact struct {
	CommandID        string
	Status           CommandStatus
	StartingAperture float64
	FinalAperture    float64
	Adjustment       float64
	ExecutedAt       time.Time
}

// AdjustmentWindowSnapshot is an explainable projection of the sliding
// adjustment window at one evaluation instant.
type AdjustmentWindowSnapshot struct {
	Config      AdjustmentWindowConfig
	Enabled     bool
	Used        float64
	Available   float64
	WindowStart time.Time
	WindowEnd   time.Time
	EvaluatedAt time.Time
	Facts       []AdjustmentFact
}

// EvaluateAdjustmentWindow computes cumulative adjustment over executed
// facts inside the half-open history interval (now-window, now]: a fact
// exactly one window old is released. Aging facts are the only mechanism
// that releases budget; there is no manual reset.
func EvaluateAdjustmentWindow(
	config AdjustmentWindowConfig,
	facts []AdjustmentFact,
	now time.Time,
) AdjustmentWindowSnapshot {
	now = now.UTC()
	snapshot := AdjustmentWindowSnapshot{
		Config:      config,
		EvaluatedAt: now,
		Facts:       []AdjustmentFact{},
	}
	if !config.Enabled() {
		return snapshot
	}

	windowStart := now.Add(-config.Duration())
	included := make([]AdjustmentFact, 0, len(facts))
	var used float64
	for _, fact := range facts {
		executedAt := fact.ExecutedAt.UTC()
		if !executedAt.After(windowStart) || executedAt.After(now) {
			continue
		}
		included = append(included, fact)
		used += fact.Adjustment
	}

	sort.SliceStable(included, func(i, j int) bool {
		if included[i].ExecutedAt.Equal(included[j].ExecutedAt) {
			return included[i].CommandID < included[j].CommandID
		}
		return included[i].ExecutedAt.Before(included[j].ExecutedAt)
	})

	available := config.MaxAdjustment - used
	if available < adjustmentEpsilon {
		available = 0
	}

	snapshot.Enabled = true
	snapshot.Facts = included
	snapshot.Used = used
	snapshot.Available = available
	snapshot.WindowStart = windowStart
	snapshot.WindowEnd = now
	return snapshot
}

// CheckAdmission rejects a prospective adjustment when accepting it would
// push cumulative adjustment past the safety limit. Equality is legal.
func (s AdjustmentWindowSnapshot) CheckAdmission(prospectiveAdjustment float64) error {
	if !s.Enabled {
		return nil
	}
	if prospectiveAdjustment > s.Available+adjustmentEpsilon {
		return Conflict(
			"adjustment_window_exceeded",
			"cumulative aperture adjustment within the duty window would exceed the safety limit",
		)
	}
	return nil
}

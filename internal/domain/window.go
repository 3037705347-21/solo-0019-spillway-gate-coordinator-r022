package domain

import (
	"fmt"
	"math"
	"time"
)

// adjustmentEpsilon keeps exact-limit boundary arithmetic stable when the
// running total is accumulated from floating point aperture deltas.
const adjustmentEpsilon = 1e-9

// MaxAdjustmentWindowSeconds bounds the configurable sliding window so a
// misconfigured request cannot ask for an effectively unbounded duration.
const MaxAdjustmentWindowSeconds = 365 * 24 * 60 * 60

// AdjustmentOutcome describes how an executed adjustment settled.
type AdjustmentOutcome string

const (
	AdjustmentCompleted AdjustmentOutcome = "completed"
	AdjustmentAborted   AdjustmentOutcome = "aborted"
)

// AdjustmentFact is an already-effective execution fact: the gate physically
// moved from StartingAperture to FinalAperture when the owning command
// settled. Pending, approved and rejected commands never produce facts.
type AdjustmentFact struct {
	GateID           string
	CommandID        string
	StartingAperture float64
	FinalAperture    float64
	Amount           float64
	Outcome          AdjustmentOutcome
	EffectiveAt      time.Time
}

func NewAdjustmentFact(
	gateID string,
	commandID string,
	startingAperture float64,
	finalAperture float64,
	outcome AdjustmentOutcome,
	effectiveAt time.Time,
) AdjustmentFact {
	return AdjustmentFact{
		GateID:           gateID,
		CommandID:        commandID,
		StartingAperture: startingAperture,
		FinalAperture:    finalAperture,
		Amount:           AdjustmentAmount(startingAperture, finalAperture),
		Outcome:          outcome,
		EffectiveAt:      effectiveAt.UTC(),
	}
}

// AdjustmentAmount is the absolute aperture travel an adjustment represents.
// The structural load depends on travel distance, not on direction.
func AdjustmentAmount(fromAperture, toAperture float64) float64 {
	return math.Abs(toAperture - fromAperture)
}

// WindowStatus is the explainable projection of one gate's sliding
// adjustment window at a single point in time.
type WindowStatus struct {
	Enabled        bool
	Window         time.Duration
	Limit          float64
	Used           float64
	Remaining      float64
	EffectiveFacts []AdjustmentFact
	ReleasedFacts  []AdjustmentFact
	EvaluatedAt    time.Time
}

// PartitionAdjustmentFacts splits effective execution facts into facts still
// inside the sliding window ending at now and facts that have aged out and
// released their capacity. The cutoff is exclusive: a fact whose age equals
// the window length has already been released.
func PartitionAdjustmentFacts(
	facts []AdjustmentFact,
	window time.Duration,
	now time.Time,
) (effective []AdjustmentFact, released []AdjustmentFact) {
	effective = make([]AdjustmentFact, 0, len(facts))
	released = make([]AdjustmentFact, 0)
	cutoff := now.Add(-window)
	for _, fact := range facts {
		if fact.EffectiveAt.After(cutoff) {
			effective = append(effective, fact)
		} else {
			released = append(released, fact)
		}
	}
	return effective, released
}

func SumAdjustmentAmounts(facts []AdjustmentFact) float64 {
	var total float64
	for _, fact := range facts {
		total += fact.Amount
	}
	return total
}

// CheckAdjustmentWindow admits a candidate only when the already-effective
// travel plus the candidate travel stays within the safety limit. The exact
// boundary is legal; only a strictly larger total is rejected.
func CheckAdjustmentWindow(used float64, candidate float64, limit float64) error {
	if used+candidate > limit+adjustmentEpsilon {
		return Conflict(
			"adjustment_window_exceeded",
			fmt.Sprintf(
				"aperture adjustment window exceeded: used %.6g plus candidate %.6g would exceed limit %.6g",
				used,
				candidate,
				limit,
			),
		)
	}
	return nil
}

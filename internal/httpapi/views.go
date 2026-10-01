package httpapi

import (
	"time"

	"example.com/spillway-gate-coordinator/internal/contracts"
	"example.com/spillway-gate-coordinator/internal/domain"
)

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func gateView(gate domain.Gate) contracts.GateView {
	return contracts.GateView{
		ID:                      gate.ID,
		Name:                    gate.Name,
		Status:                  string(gate.Status),
		Aperture:                gate.Aperture,
		MaxAperture:             gate.MaxAperture,
		ActiveCommandID:         gate.ActiveCommandID,
		AdjustmentWindowSeconds: gate.AdjustmentWindowSeconds,
		AdjustmentWindowLimit:   gate.AdjustmentWindowLimit,
		UpdatedAt:               formatTime(gate.UpdatedAt),
		Version:                 gate.Version,
	}
}

func commandView(command domain.GateCommand, includeToken bool) contracts.CommandView {
	view := contracts.CommandView{
		ID:               command.ID,
		GateID:           command.GateID,
		TargetAperture:   command.TargetAperture,
		StartingAperture: command.StartingAperture,
		Reason:           command.Reason,
		RequestedBy:      command.RequestedBy,
		Status:           string(command.Status),
		ExecutedBy:       command.ExecutedBy,
		TerminalReason:   command.TerminalReason,
		CreatedAt:        formatTime(command.CreatedAt),
		UpdatedAt:        formatTime(command.UpdatedAt),
		Version:          command.Version,
	}

	if includeToken && command.Status == domain.CommandApproved {
		view.ExecutionToken = command.ExecutionToken
	}
	if command.Interlock != nil {
		view.Interlock = &contracts.InterlockDecisionView{
			Verdict:    string(command.Interlock.Verdict),
			Verifier:   command.Interlock.Verifier,
			Note:       command.Interlock.Note,
			ReviewedAt: formatTime(command.Interlock.ReviewedAt),
		}
	}
	return view
}

func adjustmentFactViews(facts []domain.AdjustmentFact) []contracts.AdjustmentFactView {
	views := make([]contracts.AdjustmentFactView, 0, len(facts))
	for _, fact := range facts {
		views = append(views, contracts.AdjustmentFactView{
			CommandID:        fact.CommandID,
			StartingAperture: fact.StartingAperture,
			FinalAperture:    fact.FinalAperture,
			Amount:           fact.Amount,
			Outcome:          string(fact.Outcome),
			EffectiveAt:      formatTime(fact.EffectiveAt),
		})
	}
	return views
}

func adjustmentWindowView(gateID string, status domain.WindowStatus) contracts.AdjustmentWindowView {
	view := contracts.AdjustmentWindowView{
		GateID:      gateID,
		Enabled:     status.Enabled,
		Window:      map[string]any{},
		Effective:   adjustmentFactViews(status.EffectiveFacts),
		Released:    adjustmentFactViews(status.ReleasedFacts),
		EvaluatedAt: formatTime(status.EvaluatedAt),
	}
	if status.Enabled {
		view.Window["seconds"] = int64(status.Window / time.Second)
		view.Window["limit"] = status.Limit
		view.Used = status.Used
		view.Remaining = status.Remaining
	}
	return view
}

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
	view := contracts.GateView{
		ID:                      gate.ID,
		Name:                    gate.Name,
		Status:                  string(gate.Status),
		Aperture:                gate.Aperture,
		MaxAperture:             gate.MaxAperture,
		AdjustmentWindowEnabled: gate.AdjustmentWindow.Enabled(),
		ActiveCommandID:         gate.ActiveCommandID,
		UpdatedAt:               formatTime(gate.UpdatedAt),
		Version:                 gate.Version,
	}
	if gate.AdjustmentWindow.Enabled() {
		view.AdjustmentWindowSeconds = gate.AdjustmentWindow.WindowSeconds
		view.MaxWindowAdjustment = gate.AdjustmentWindow.MaxAdjustment
	}
	return view
}

func adjustmentWindowView(gate domain.Gate, snapshot domain.AdjustmentWindowSnapshot) contracts.AdjustmentWindowView {
	view := contracts.AdjustmentWindowView{
		GateID:      gate.ID,
		Enabled:     snapshot.Enabled,
		Used:        snapshot.Used,
		EvaluatedAt: formatTime(snapshot.EvaluatedAt),
		Facts:       []contracts.AdjustmentFactView{},
	}
	if snapshot.Enabled {
		view.WindowSeconds = snapshot.Config.WindowSeconds
		view.Limit = snapshot.Config.MaxAdjustment
		view.Available = snapshot.Available
		view.WindowStart = formatTime(snapshot.WindowStart)
		view.WindowEnd = formatTime(snapshot.WindowEnd)
		for _, fact := range snapshot.Facts {
			view.Facts = append(view.Facts, contracts.AdjustmentFactView{
				CommandID:        fact.CommandID,
				Status:           string(fact.Status),
				StartingAperture: fact.StartingAperture,
				FinalAperture:    fact.FinalAperture,
				Adjustment:       fact.Adjustment,
				ExecutedAt:       formatTime(fact.ExecutedAt),
			})
		}
	}
	return view
}

func commandView(command domain.GateCommand, includeToken bool) contracts.CommandView {
	view := contracts.CommandView{
		ID:               command.ID,
		GateID:           command.GateID,
		TargetAperture:   command.TargetAperture,
		StartingAperture: command.StartingAperture,
		FinalAperture:    command.FinalAperture,
		Adjustment:       command.Adjustment,
		Reason:           command.Reason,
		RequestedBy:      command.RequestedBy,
		Status:           string(command.Status),
		ExecutedBy:       command.ExecutedBy,
		TerminalReason:   command.TerminalReason,
		CreatedAt:        formatTime(command.CreatedAt),
		UpdatedAt:        formatTime(command.UpdatedAt),
		Version:          command.Version,
	}

	if command.ExecutedAt != nil {
		view.ExecutedAt = formatTime(*command.ExecutedAt)
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

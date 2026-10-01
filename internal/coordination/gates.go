package coordination

import (
	"context"

	"example.com/spillway-gate-coordinator/internal/contracts"
	"example.com/spillway-gate-coordinator/internal/domain"
)

func (s *Service) RegisterGate(ctx context.Context, request contracts.RegisterGateRequest) (domain.Gate, error) {
	gate, err := domain.NewGate(
		request.ID,
		request.Name,
		request.MaxAperture,
		request.InitialAperture,
		request.AdjustmentWindowSeconds,
		request.AdjustmentWindowLimit,
		s.now(),
	)
	if err != nil {
		return domain.Gate{}, err
	}
	if err := s.repository.CreateGate(ctx, gate); err != nil {
		return domain.Gate{}, err
	}
	return gate, nil
}

func (s *Service) Gate(ctx context.Context, id string) (domain.Gate, error) {
	return s.repository.Gate(ctx, id)
}

// AdjustmentWindow projects the gate's sliding adjustment window from
// already-effective execution facts. Effective and released facts are both
// returned so legal boundaries and real releases stay explainable.
func (s *Service) AdjustmentWindow(ctx context.Context, gateID string) (domain.WindowStatus, error) {
	now := s.now()
	gate, facts, err := s.repository.AdjustmentWindow(ctx, gateID)
	if err != nil {
		return domain.WindowStatus{}, err
	}

	status := domain.WindowStatus{
		Enabled:     gate.AdjustmentWindowEnabled(),
		EvaluatedAt: now.UTC(),
	}
	if !status.Enabled {
		return status, nil
	}

	status.Window = gate.AdjustmentWindow()
	status.Limit = gate.AdjustmentWindowLimit
	status.EffectiveFacts, status.ReleasedFacts = domain.PartitionAdjustmentFacts(
		facts,
		status.Window,
		now,
	)
	status.Used = domain.SumAdjustmentAmounts(status.EffectiveFacts)
	status.Remaining = status.Limit - status.Used
	if status.Remaining < 0 {
		// Cannot happen through admission, but keeps the projection honest.
		status.Remaining = 0
	}
	return status, nil
}

package coordination

import (
	"context"

	"example.com/spillway-gate-coordinator/internal/contracts"
	"example.com/spillway-gate-coordinator/internal/domain"
)

func (s *Service) RequestCommand(
	ctx context.Context,
	request contracts.RequestCommandRequest,
) (domain.GateCommand, error) {
	gate, err := s.repository.Gate(ctx, request.GateID)
	if err != nil {
		return domain.GateCommand{}, err
	}

	commandID, err := s.newID("cmd")
	if err != nil {
		return domain.GateCommand{}, domain.Internal("command_id_generation_failed", "command id could not be generated")
	}

	command, err := domain.NewGateCommand(
		commandID,
		gate.ID,
		request.TargetAperture,
		gate.Aperture,
		gate.MaxAperture,
		request.Reason,
		request.RequestedBy,
		s.now(),
	)
	if err != nil {
		return domain.GateCommand{}, err
	}
	// The window is re-checked by the repository while it holds the write
	// lock, against effective facts only. A request that would exceed the
	// limit is rejected here without ever storing the command.
	if err := s.repository.CreateCommand(ctx, command, s.now()); err != nil {
		return domain.GateCommand{}, err
	}
	return command, nil
}

func (s *Service) Command(ctx context.Context, id string) (domain.GateCommand, error) {
	return s.repository.Command(ctx, id)
}

func (s *Service) ReviewInterlock(
	ctx context.Context,
	commandID string,
	request contracts.ReviewInterlockRequest,
) (domain.GateCommand, domain.Gate, error) {
	decision, err := domain.NewInterlockDecision(
		request.Verdict,
		request.Verifier,
		request.Note,
		s.now(),
	)
	if err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	// The token is issued inside the repository critical section, only after
	// the sliding-window admission and the gate reservation have both
	// succeeded, so a window rejection never mints or stores a credential.
	return s.repository.ReviewCommand(ctx, commandID, decision, s.newToken, s.now())
}

func (s *Service) ExecuteCommand(
	ctx context.Context,
	commandID string,
	request contracts.ExecuteCommandRequest,
) (domain.GateCommand, domain.Gate, error) {
	return s.repository.ExecuteCommand(
		ctx,
		commandID,
		request.Token,
		request.Operator,
		request.SimulateFault,
		s.now(),
	)
}

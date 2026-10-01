package storage

import (
	"context"

	"example.com/spillway-gate-coordinator/internal/domain"
)

type Repository interface {
	CreateGate(ctx context.Context, gate domain.Gate) error
	Gate(ctx context.Context, id string) (domain.Gate, error)
	CreateCommand(ctx context.Context, command domain.GateCommand) error
	Command(ctx context.Context, id string) (domain.GateCommand, error)
	ReviewCommand(
		ctx context.Context,
		commandID string,
		decision domain.InterlockDecision,
		mintToken func() (string, error),
	) (domain.GateCommand, domain.Gate, error)
	AdjustmentWindow(ctx context.Context, gateID string) (domain.Gate, domain.AdjustmentWindowSnapshot, error)
	ExecuteCommand(
		ctx context.Context,
		commandID string,
		token string,
		operator string,
		simulateFault bool,
	) (domain.GateCommand, domain.Gate, error)
}

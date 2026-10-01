package storage

import (
	"context"
	"sync"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
)

type MemoryRepository struct {
	mu              sync.RWMutex
	gates           map[string]domain.Gate
	commands        map[string]domain.GateCommand
	adjustmentFacts map[string][]domain.AdjustmentFact
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		gates:           make(map[string]domain.Gate),
		commands:        make(map[string]domain.GateCommand),
		adjustmentFacts: make(map[string][]domain.AdjustmentFact),
	}
}

func (m *MemoryRepository) CreateGate(_ context.Context, gate domain.Gate) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.gates[gate.ID]; exists {
		return domain.Conflict("duplicate_gate_id", "gate id already exists")
	}
	m.gates[gate.ID] = gate
	return nil
}

func (m *MemoryRepository) Gate(_ context.Context, id string) (domain.Gate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	gate, exists := m.gates[id]
	if !exists {
		return domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}
	return gate, nil
}

func (m *MemoryRepository) CreateCommand(_ context.Context, command domain.GateCommand, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	gate, exists := m.gates[command.GateID]
	if !exists {
		return domain.NotFound("gate_not_found", "gate id was not found")
	}
	if gate.Status == domain.GateFaulted {
		return domain.Conflict("gate_faulted", "faulted gates cannot receive new commands")
	}
	if command.StartingAperture != gate.Aperture {
		return domain.Conflict("gate_aperture_changed", "gate aperture changed before the command was stored")
	}
	if command.TargetAperture > gate.MaxAperture {
		return domain.Invalid("invalid_target_aperture", "target aperture is outside the gate limit")
	}
	if err := checkGateWindow(&gate, m.adjustmentFacts[gate.ID], command.TargetAperture, gate.Aperture, now); err != nil {
		return err
	}
	if _, exists := m.commands[command.ID]; exists {
		return domain.Conflict("duplicate_command_id", "command id already exists")
	}

	m.commands[command.ID] = command
	return nil
}

func (m *MemoryRepository) Command(_ context.Context, id string) (domain.GateCommand, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	command, exists := m.commands[id]
	if !exists {
		return domain.GateCommand{}, domain.NotFound("command_not_found", "command id was not found")
	}
	return cloneCommand(command), nil
}

func (m *MemoryRepository) ReviewCommand(
	_ context.Context,
	commandID string,
	decision domain.InterlockDecision,
	issueToken func() (string, error),
	now time.Time,
) (domain.GateCommand, domain.Gate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	command, exists := m.commands[commandID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("command_not_found", "command id was not found")
	}
	gate, exists := m.gates[command.GateID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}

	nextCommand := cloneCommand(command)
	nextGate := gate

	if decision.IsClear() {
		// Reservation and window admission share this repository lock, so a
		// concurrently executing command cannot settle travel or release the
		// gate between either decision and the commit.
		if err := nextGate.Reserve(nextCommand.ID, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		if err := checkGateWindow(
			&nextGate,
			m.adjustmentFacts[nextGate.ID],
			nextCommand.TargetAperture,
			nextGate.Aperture,
			now,
		); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}

		// The credential is issued only after the gate reservation and the
		// window admission have both succeeded, so a rejected request never
		// receives or stores a token.
		token, err := issueToken()
		if err != nil {
			return domain.GateCommand{}, domain.Gate{}, domain.Internal(
				"execution_token_generation_failed",
				"execution token could not be generated",
			)
		}
		if err := nextCommand.Review(decision, token, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	} else {
		// Blocked reviews do not touch the window or the reservation.
		if err := nextCommand.Review(decision, "", now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	}

	m.commands[nextCommand.ID] = nextCommand
	m.gates[nextGate.ID] = nextGate
	return cloneCommand(nextCommand), nextGate, nil
}

func (m *MemoryRepository) ExecuteCommand(
	_ context.Context,
	commandID string,
	token string,
	operator string,
	simulateFault bool,
	now time.Time,
) (domain.GateCommand, domain.Gate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	command, exists := m.commands[commandID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("command_not_found", "command id was not found")
	}
	gate, exists := m.gates[command.GateID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}

	// The aperture before movement is the real base of the executed fact.
	// A clear review can only reserve the gate while it is idle, so no
	// other command can have moved it in between.
	startingAperture := gate.Aperture

	nextCommand := cloneCommand(command)
	if err := nextCommand.BeginExecution(token, operator, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	nextGate := gate
	if err := nextGate.StartMovement(nextCommand.ID, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	outcome := domain.AdjustmentCompleted
	if simulateFault {
		if err := nextGate.Settle(nextCommand.TargetAperture, true, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		if err := nextCommand.Abort("simulated_gate_fault", now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		outcome = domain.AdjustmentAborted
	} else {
		if err := nextGate.Settle(nextCommand.TargetAperture, false, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		if err := nextCommand.Complete(now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	}

	// The executed travel becomes an effective fact at settle time, inside
	// the same critical section as the gate and command writes.
	fact := domain.NewAdjustmentFact(
		nextGate.ID,
		nextCommand.ID,
		startingAperture,
		nextGate.Aperture,
		outcome,
		now,
	)
	m.adjustmentFacts[nextGate.ID] = append(m.adjustmentFacts[nextGate.ID], fact)

	m.commands[nextCommand.ID] = nextCommand
	m.gates[nextGate.ID] = nextGate
	return cloneCommand(nextCommand), nextGate, nil
}

// AdjustmentWindow returns the gate and its full effective-execution history
// in one locked snapshot so the caller projects a coherent window view.
func (m *MemoryRepository) AdjustmentWindow(_ context.Context, gateID string) (domain.Gate, []domain.AdjustmentFact, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	gate, exists := m.gates[gateID]
	if !exists {
		return domain.Gate{}, nil, domain.NotFound("gate_not_found", "gate id was not found")
	}
	facts := m.adjustmentFacts[gateID]
	factsCopy := make([]domain.AdjustmentFact, len(facts))
	copy(factsCopy, facts)
	return gate, factsCopy, nil
}

// checkGateWindow must be called with m.mu held. It counts only facts that
// are still inside the gate's sliding window at now and rejects the
// candidate when the cumulative travel would cross the safety limit.
func checkGateWindow(
	gate *domain.Gate,
	facts []domain.AdjustmentFact,
	targetAperture float64,
	currentAperture float64,
	now time.Time,
) error {
	if !gate.AdjustmentWindowEnabled() {
		return nil
	}
	effective, _ := domain.PartitionAdjustmentFacts(facts, gate.AdjustmentWindow(), now)
	used := domain.SumAdjustmentAmounts(effective)
	candidate := domain.AdjustmentAmount(currentAperture, targetAperture)
	return domain.CheckAdjustmentWindow(used, candidate, gate.AdjustmentWindowLimit)
}

func cloneCommand(command domain.GateCommand) domain.GateCommand {
	if command.Interlock == nil {
		return command
	}
	decision := *command.Interlock
	command.Interlock = &decision
	return command
}

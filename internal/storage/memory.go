package storage

import (
	"context"
	"sync"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
)

type MemoryRepository struct {
	mu       sync.RWMutex
	now      func() time.Time
	gates    map[string]domain.Gate
	commands map[string]domain.GateCommand
}

func NewMemoryRepository() *MemoryRepository {
	return NewMemoryRepositoryWithClock(time.Now)
}

// NewMemoryRepositoryWithClock exists for deterministic tests. Production
// code uses NewMemoryRepository.
func NewMemoryRepositoryWithClock(now func() time.Time) *MemoryRepository {
	return &MemoryRepository{
		now:      now,
		gates:    make(map[string]domain.Gate),
		commands: make(map[string]domain.GateCommand),
	}
}

// nowLocked returns the critical-section clock. Every fact timestamp and
// every window evaluation must be taken while holding m.mu, so a caller
// queued behind another transaction can never judge a fact committed in that
// other transaction as "in the future".
func (m *MemoryRepository) nowLocked() time.Time {
	return m.now().UTC()
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

func (m *MemoryRepository) CreateCommand(_ context.Context, command domain.GateCommand) error {
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
	if _, exists := m.commands[command.ID]; exists {
		return domain.Conflict("duplicate_command_id", "command id already exists")
	}

	// Window admission runs in the same critical section that stores the
	// command, against only effective execution facts stamped in this same
	// clock domain. A rejected request returns before any map write, so it
	// cannot occupy a pending slot or otherwise advance state.
	now := m.nowLocked()
	snapshot := m.windowSnapshotLocked(gate, now)
	prospective := domain.AdjustmentMagnitude(command.StartingAperture, command.TargetAperture)
	if err := snapshot.CheckAdmission(prospective); err != nil {
		return err
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
	mintToken func() (string, error),
) (domain.GateCommand, domain.Gate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// The authoritative transition instant is taken inside the critical
	// section. Using a caller-supplied pre-lock instant here would let a
	// fact committed by another goroutine while this one queued on the mutex
	// look like a future event and vanish from the window projection.
	now := m.nowLocked()
	decision.ReviewedAt = now

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
		// Admission, reservation and credential issuance are one atomic
		// step. The gate snapshot used for window accounting is the same
		// value reserved below; any failure returns before any map write,
		// leaving command and gate byte-for-byte unchanged (no token, no
		// version bump, no active command reference).
		if err := nextCommand.CanReportClear(); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}

		snapshot := m.windowSnapshotLocked(nextGate, now)
		prospective := domain.AdjustmentMagnitude(nextGate.Aperture, nextCommand.TargetAperture)
		if err := snapshot.CheckAdmission(prospective); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		if err := nextGate.Reserve(nextCommand.ID, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}

		token, err := mintToken()
		if err != nil {
			return domain.GateCommand{}, domain.Gate{}, domain.Internal(
				"execution_token_generation_failed",
				"execution token could not be generated",
			)
		}
		if err := nextCommand.Review(decision, token, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}

		m.commands[nextCommand.ID] = nextCommand
		m.gates[nextGate.ID] = nextGate
		return cloneCommand(nextCommand), nextGate, nil
	}

	if err := nextCommand.Review(decision, "", now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}
	m.commands[nextCommand.ID] = nextCommand
	return cloneCommand(nextCommand), gate, nil
}

func (m *MemoryRepository) AdjustmentWindow(
	_ context.Context,
	gateID string,
) (domain.Gate, domain.AdjustmentWindowSnapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	gate, exists := m.gates[gateID]
	if !exists {
		return domain.Gate{}, domain.AdjustmentWindowSnapshot{}, domain.NotFound("gate_not_found", "gate id was not found")
	}
	return gate, m.windowSnapshotLocked(gate, m.nowLocked()), nil
}

func (m *MemoryRepository) ExecuteCommand(
	_ context.Context,
	commandID string,
	token string,
	operator string,
	simulateFault bool,
) (domain.GateCommand, domain.Gate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.nowLocked()

	command, exists := m.commands[commandID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("command_not_found", "command id was not found")
	}
	gate, exists := m.gates[command.GateID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}

	nextCommand := cloneCommand(command)
	if err := nextCommand.BeginExecution(token, operator, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	nextGate := gate
	movementStartAperture := gate.Aperture
	if err := nextGate.StartMovement(nextCommand.ID, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	if simulateFault {
		if err := nextGate.Settle(nextCommand.TargetAperture, true, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		// The gate physically moved before faulting; record the fact before
		// the command reaches its terminal state.
		if err := nextCommand.RecordExecutionResult(movementStartAperture, nextCommand.TargetAperture, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		if err := nextCommand.Abort("simulated_gate_fault", now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	} else {
		if err := nextGate.Settle(nextCommand.TargetAperture, false, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		if err := nextCommand.RecordExecutionResult(movementStartAperture, nextCommand.TargetAperture, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		if err := nextCommand.Complete(now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	}

	m.commands[nextCommand.ID] = nextCommand
	m.gates[nextGate.ID] = nextGate
	return cloneCommand(nextCommand), nextGate, nil
}

// windowSnapshotLocked builds the duty-window projection from executed
// commands only. The passed now must be the current transaction's
// critical-section instant. Callers must hold m.mu (read lock suffices).
func (m *MemoryRepository) windowSnapshotLocked(gate domain.Gate, now time.Time) domain.AdjustmentWindowSnapshot {
	facts := make([]domain.AdjustmentFact, 0)
	for _, command := range m.commands {
		if command.GateID != gate.ID || command.ExecutedAt == nil {
			continue
		}
		if command.Status != domain.CommandCompleted && command.Status != domain.CommandAborted {
			continue
		}
		facts = append(facts, domain.AdjustmentFact{
			CommandID:        command.ID,
			Status:           command.Status,
			StartingAperture: command.StartingAperture,
			FinalAperture:    command.FinalAperture,
			Adjustment:       command.Adjustment,
			ExecutedAt:       *command.ExecutedAt,
		})
	}
	return domain.EvaluateAdjustmentWindow(gate.AdjustmentWindow, facts, now)
}

func cloneCommand(command domain.GateCommand) domain.GateCommand {
	if command.Interlock != nil {
		decision := *command.Interlock
		command.Interlock = &decision
	}
	if command.ExecutedAt != nil {
		executedAt := *command.ExecutedAt
		command.ExecutedAt = &executedAt
	}
	return command
}

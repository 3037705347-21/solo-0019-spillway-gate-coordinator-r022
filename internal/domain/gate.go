package domain

import (
	"regexp"
	"strings"
	"time"
)

type GateStatus string

const (
	GateIdle     GateStatus = "idle"
	GateReserved GateStatus = "reserved"
	GateMoving   GateStatus = "moving"
	GateFaulted  GateStatus = "faulted"
)

var gateIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,31}$`)

type Gate struct {
	ID                      string
	Name                    string
	Status                  GateStatus
	Aperture                float64
	MaxAperture             float64
	ActiveCommandID         string
	AdjustmentWindowSeconds int64
	AdjustmentWindowLimit   float64
	UpdatedAt               time.Time
	Version                 int64
}

// AdjustmentWindowEnabled reports whether the gate caps cumulative aperture
// travel inside a sliding window. Both settings must be present together.
func (g Gate) AdjustmentWindowEnabled() bool {
	return g.AdjustmentWindowSeconds > 0 && g.AdjustmentWindowLimit > 0
}

func (g Gate) AdjustmentWindow() time.Duration {
	return time.Duration(g.AdjustmentWindowSeconds) * time.Second
}

func NewGate(
	id, name string,
	maxAperture, initialAperture float64,
	adjustmentWindowSeconds int64,
	adjustmentWindowLimit float64,
	now time.Time,
) (Gate, error) {
	id = strings.TrimSpace(id)
	if !gateIDPattern.MatchString(id) {
		return Gate{}, Invalid("invalid_gate_id", "gate id must use lowercase letters, digits, and hyphens")
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return Gate{}, Invalid("invalid_gate_name", "gate name is required")
	}
	if maxAperture <= 0 || maxAperture > 1000 {
		return Gate{}, Invalid("invalid_max_aperture", "max aperture must be greater than zero")
	}
	if initialAperture < 0 || initialAperture > maxAperture {
		return Gate{}, Invalid("invalid_initial_aperture", "initial aperture must be within the gate limit")
	}
	if err := validateAdjustmentWindow(adjustmentWindowSeconds, adjustmentWindowLimit); err != nil {
		return Gate{}, err
	}

	return Gate{
		ID:                      id,
		Name:                    name,
		Status:                  GateIdle,
		Aperture:                initialAperture,
		MaxAperture:             maxAperture,
		AdjustmentWindowSeconds: adjustmentWindowSeconds,
		AdjustmentWindowLimit:   adjustmentWindowLimit,
		UpdatedAt:               now.UTC(),
		Version:                 1,
	}, nil
}

// validateAdjustmentWindow requires the window duration and the cumulative
// travel limit to be configured together. A gate without either setting
// simply runs without window enforcement.
func validateAdjustmentWindow(windowSeconds int64, windowLimit float64) error {
	if windowSeconds < 0 {
		return Invalid("invalid_adjustment_window_seconds", "adjustment window seconds cannot be negative")
	}
	if windowLimit < 0 {
		return Invalid("invalid_adjustment_window_limit", "adjustment window limit cannot be negative")
	}
	hasWindow := windowSeconds > 0
	hasLimit := windowLimit > 0
	if !hasWindow && !hasLimit {
		return nil
	}
	if !hasWindow {
		return Invalid("invalid_adjustment_window_seconds", "adjustment window seconds must be greater than zero when a limit is set")
	}
	if windowSeconds > MaxAdjustmentWindowSeconds {
		return Invalid("invalid_adjustment_window_seconds", "adjustment window seconds exceed the allowed maximum")
	}
	if !hasLimit {
		return Invalid("invalid_adjustment_window_limit", "adjustment window limit must be greater than zero when a window is set")
	}
	return nil
}

func (g *Gate) Reserve(commandID string, now time.Time) error {
	if g.Status != GateIdle {
		return Conflict("gate_not_idle", "gate is not available for a new command")
	}
	if g.ActiveCommandID != "" {
		return Conflict("gate_already_reserved", "gate already references an active command")
	}
	if strings.TrimSpace(commandID) == "" {
		return Invalid("invalid_active_command", "active command id is required")
	}

	g.Status = GateReserved
	g.ActiveCommandID = commandID
	g.UpdatedAt = now.UTC()
	g.Version++
	return nil
}

func (g *Gate) StartMovement(commandID string, now time.Time) error {
	if g.Status != GateReserved {
		return Conflict("gate_not_reserved", "gate must be reserved before movement starts")
	}
	if g.ActiveCommandID != commandID {
		return Conflict("gate_command_mismatch", "gate is reserved by a different command")
	}

	g.Status = GateMoving
	g.UpdatedAt = now.UTC()
	g.Version++
	return nil
}

func (g *Gate) Settle(finalAperture float64, faulted bool, now time.Time) error {
	if g.Status != GateMoving {
		return Conflict("gate_not_moving", "gate must be moving before it can settle")
	}
	if finalAperture < 0 || finalAperture > g.MaxAperture {
		return Invalid("invalid_final_aperture", "final aperture must be within the gate limit")
	}

	g.Aperture = finalAperture
	g.ActiveCommandID = ""
	if faulted {
		g.Status = GateFaulted
	} else {
		g.Status = GateIdle
	}
	g.UpdatedAt = now.UTC()
	g.Version++
	return nil
}

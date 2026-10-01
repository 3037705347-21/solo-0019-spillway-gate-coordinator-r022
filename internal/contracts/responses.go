package contracts

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type InterlockDecisionView struct {
	Verdict    string `json:"verdict"`
	Verifier   string `json:"verifier"`
	Note       string `json:"note"`
	ReviewedAt string `json:"reviewed_at"`
}

type GateView struct {
	ID                      string  `json:"id"`
	Name                    string  `json:"name"`
	Status                  string  `json:"status"`
	Aperture                float64 `json:"aperture"`
	MaxAperture             float64 `json:"max_aperture"`
	AdjustmentWindowEnabled bool    `json:"adjustment_window_enabled"`
	AdjustmentWindowSeconds int64   `json:"adjustment_window_seconds,omitempty"`
	MaxWindowAdjustment     float64 `json:"max_window_adjustment,omitempty"`
	ActiveCommandID         string  `json:"active_command_id,omitempty"`
	UpdatedAt               string  `json:"updated_at"`
	Version                 int64   `json:"version"`
}

type AdjustmentFactView struct {
	CommandID        string  `json:"command_id"`
	Status           string  `json:"status"`
	StartingAperture float64 `json:"starting_aperture"`
	FinalAperture    float64 `json:"final_aperture"`
	Adjustment       float64 `json:"adjustment"`
	ExecutedAt       string  `json:"executed_at"`
}

type AdjustmentWindowView struct {
	GateID        string               `json:"gate_id"`
	Enabled       bool                 `json:"enabled"`
	WindowSeconds int64                `json:"window_seconds,omitempty"`
	Limit         float64              `json:"limit,omitempty"`
	Used          float64              `json:"used"`
	Available     float64              `json:"available,omitempty"`
	WindowStart   string               `json:"window_start,omitempty"`
	WindowEnd     string               `json:"window_end,omitempty"`
	EvaluatedAt   string               `json:"evaluated_at"`
	Facts         []AdjustmentFactView `json:"facts"`
}

type AdjustmentWindowResponse struct {
	Window AdjustmentWindowView `json:"adjustment_window"`
}

type CommandView struct {
	ID               string                 `json:"id"`
	GateID           string                 `json:"gate_id"`
	TargetAperture   float64                `json:"target_aperture"`
	StartingAperture float64                `json:"starting_aperture"`
	FinalAperture    float64                `json:"final_aperture,omitempty"`
	Adjustment       float64                `json:"adjustment,omitempty"`
	Reason           string                 `json:"reason"`
	RequestedBy      string                 `json:"requested_by"`
	Status           string                 `json:"status"`
	Interlock        *InterlockDecisionView `json:"interlock,omitempty"`
	ExecutionToken   string                 `json:"execution_token,omitempty"`
	ExecutedBy       string                 `json:"executed_by,omitempty"`
	TerminalReason   string                 `json:"terminal_reason,omitempty"`
	ExecutedAt       string                 `json:"executed_at,omitempty"`
	CreatedAt        string                 `json:"created_at"`
	UpdatedAt        string                 `json:"updated_at"`
	Version          int64                  `json:"version"`
}

type RegisterGateResponse struct {
	Gate GateView `json:"gate"`
}

type GateResponse struct {
	Gate GateView `json:"gate"`
}

type CommandResponse struct {
	Command CommandView `json:"command"`
}

type ReviewCommandResponse struct {
	Command CommandView `json:"command"`
	Gate    GateView    `json:"gate"`
}

type ExecuteCommandResponse struct {
	Command CommandView `json:"command"`
	Gate    GateView    `json:"gate"`
}

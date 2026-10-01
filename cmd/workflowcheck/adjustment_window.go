package main

import (
	"fmt"
	"net/http"
)

func boolField(object map[string]any, name string) (bool, error) {
	value, exists := object[name]
	if !exists {
		return false, fmt.Errorf("object is missing boolean field %q", name)
	}
	boolean, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("object field %q is not a boolean", name)
	}
	return boolean, nil
}

func arrayField(object map[string]any, name string) ([]any, error) {
	value, exists := object[name]
	if !exists {
		return nil, fmt.Errorf("object is missing array field %q", name)
	}
	array, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("object field %q is not an array", name)
	}
	return array, nil
}

func runAdjustmentWindow(client *apiClient) error {
	created, err := fixtureGateWithWindow(client, "gate-duty", "值班窗口闸门", 5, 0.5, 3600, 1.0)
	if err != nil {
		return err
	}
	enabled, err := boolField(created, "adjustment_window_enabled")
	if err != nil {
		return err
	}
	if !enabled {
		return fmt.Errorf("registered gate should report an enabled adjustment window")
	}
	windowSeconds, err := floatField(created, "adjustment_window_seconds")
	if err != nil || windowSeconds != 3600 {
		return fmt.Errorf("window seconds mismatch: %v / %v", windowSeconds, err)
	}
	limit, err := floatField(created, "max_window_adjustment")
	if err != nil || limit != 1.0 {
		return fmt.Errorf("window limit mismatch: %v / %v", limit, err)
	}

	// A half-configured window (duration without a limit) is rejected up front.
	halfConfigured, err := client.post("/gates", map[string]any{
		"id":                        "gate-half",
		"name":                      "半配置窗口",
		"max_aperture":              5,
		"initial_aperture":          0,
		"adjustment_window_seconds": 600,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(halfConfigured, http.StatusBadRequest, "reject half-configured window"); err != nil {
		return err
	}
	if code, _ := stringField(halfConfigured.Body, "code"); code != "invalid_adjustment_window" {
		return fmt.Errorf("half-configured window error code mismatch: %s", code)
	}

	window, err := getWindow(client, "gate-duty")
	if err != nil {
		return err
	}
	if used, _ := floatField(window, "used"); used != 0 {
		return fmt.Errorf("fresh window used: expected 0, got %v", used)
	}
	if available, _ := floatField(window, "available"); available != 1.0 {
		return fmt.Errorf("fresh window available: expected 1, got %v", available)
	}
	if facts, _ := arrayField(window, "facts"); len(facts) != 0 {
		return fmt.Errorf("fresh window must list no facts, got %d", len(facts))
	}

	// First effective fact: 0.5 -> 1.0 consumes exactly 0.5.
	first, err := fixtureCommand(client, "gate-duty", 1.0, "第一次窗口调整", "dispatcher-lin")
	if err != nil {
		return err
	}
	firstID, _ := stringField(first, "id")
	approved, err := fixtureClearReview(client, firstID, "safety-wu")
	if err != nil {
		return err
	}
	token, _ := stringField(approved, "execution_token")
	executed, err := client.post("/commands/"+firstID+"/execute", map[string]any{
		"operator":       "operator-chen",
		"token":          token,
		"simulate_fault": false,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(executed, http.StatusOK, "execute first window command"); err != nil {
		return err
	}
	executedCommand, err := objectField(executed.Body, "command")
	if err != nil {
		return err
	}
	if final, _ := floatField(executedCommand, "final_aperture"); final != 1.0 {
		return fmt.Errorf("completed command final aperture: expected 1, got %v", final)
	}
	if adjustment, _ := floatField(executedCommand, "adjustment"); adjustment != 0.5 {
		return fmt.Errorf("completed command adjustment: expected 0.5, got %v", adjustment)
	}
	if executedAt, _ := stringField(executedCommand, "executed_at"); executedAt == "" {
		return fmt.Errorf("completed command must record executed_at")
	}

	gateAfterFirst, err := getGate(client, "gate-duty")
	if err != nil {
		return err
	}
	gateVersionAfterFirst, _ := floatField(gateAfterFirst, "version")

	window, err = getWindow(client, "gate-duty")
	if err != nil {
		return err
	}
	if used, _ := floatField(window, "used"); used != 0.5 {
		return fmt.Errorf("used after first fact: expected 0.5, got %v", used)
	}
	if available, _ := floatField(window, "available"); available != 0.5 {
		return fmt.Errorf("available after first fact: expected 0.5, got %v", available)
	}
	facts, err := arrayField(window, "facts")
	if err != nil || len(facts) != 1 {
		return fmt.Errorf("window must list exactly one explainable fact, got %v / %v", facts, err)
	}
	fact, _ := facts[0].(map[string]any)
	if factStatus, _ := stringField(fact, "status"); factStatus != "completed" {
		return fmt.Errorf("fact status: expected completed, got %q", factStatus)
	}
	if starting, _ := floatField(fact, "starting_aperture"); starting != 0.5 {
		return fmt.Errorf("fact starting aperture: expected 0.5, got %v", starting)
	}
	if final, _ := floatField(fact, "final_aperture"); final != 1.0 {
		return fmt.Errorf("fact final aperture: expected 1, got %v", final)
	}

	// A 0.6 adjustment would take cumulative adjustment to 1.1: rejected at
	// request time, twice in a row, proving it never occupies or advances.
	for attempt := 0; attempt < 2; attempt++ {
		rejected, err := client.post("/commands", map[string]any{
			"gate_id":         "gate-duty",
			"target_aperture": 0.4,
			"reason":          "超额连续调整",
			"requested_by":    "dispatcher-lin",
		})
		if err != nil {
			return err
		}
		if err := requireStatus(rejected, http.StatusConflict, "reject over-window new request"); err != nil {
			return err
		}
		if code, _ := stringField(rejected.Body, "code"); code != "adjustment_window_exceeded" {
			return fmt.Errorf("over-window request code: expected adjustment_window_exceeded, got %q", code)
		}
	}
	gateAfterReject, err := getGate(client, "gate-duty")
	if err != nil {
		return err
	}
	if version, _ := floatField(gateAfterReject, "version"); version != gateVersionAfterFirst {
		return fmt.Errorf("rejected request advanced gate version: %v -> %v", gateVersionAfterFirst, version)
	}
	if status, _ := stringField(gateAfterReject, "status"); status != "idle" {
		return fmt.Errorf("rejected request changed gate status to %q", status)
	}
	if _, exists := gateAfterReject["active_command_id"]; exists {
		return fmt.Errorf("rejected request left an active command reference: %v", gateAfterReject["active_command_id"])
	}

	// Review-time rejection: file a pending command while 0.5 remains, let
	// another command commit the last 0.5, then the earlier clear review must
	// be rejected against the freshly executed fact (no token, stays pending).
	// The pending command is filed while the gate is at 1.0 (0.1 of travel);
	// after the window is exhausted its clear review is recomputed from the
	// live aperture 0.5 (0.4 of travel) and refused.
	pending, err := fixtureCommand(client, "gate-duty", 0.9, "延后复核的窗口申请", "dispatcher-yang")
	if err != nil {
		return err
	}
	pendingID, _ := stringField(pending, "id")

	boundary, err := fixtureCommand(client, "gate-duty", 0.5, "边界累计调整", "dispatcher-lin")
	if err != nil {
		return err
	}
	boundaryID, _ := stringField(boundary, "id")
	boundaryApproved, err := fixtureClearReview(client, boundaryID, "safety-wu")
	if err != nil {
		return err
	}
	if status, _ := stringField(boundaryApproved, "status"); status != "approved" {
		return fmt.Errorf("boundary review status: expected approved, got %q", status)
	}
	boundaryToken, _ := stringField(boundaryApproved, "execution_token")
	if boundaryToken == "" {
		return fmt.Errorf("boundary review must issue an execution token")
	}
	boundaryExecuted, err := client.post("/commands/"+boundaryID+"/execute", map[string]any{
		"operator":       "operator-chen",
		"token":          boundaryToken,
		"simulate_fault": false,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(boundaryExecuted, http.StatusOK, "execute boundary command"); err != nil {
		return err
	}

	window, err = getWindow(client, "gate-duty")
	if err != nil {
		return err
	}
	if used, _ := floatField(window, "used"); used != 1.0 {
		return fmt.Errorf("window should be exhausted at 1.0, got used=%v", used)
	}
	if available, _ := floatField(window, "available"); available != 0 {
		return fmt.Errorf("exhausted window available: expected 0, got %v", available)
	}

	lateReview, err := client.post("/commands/"+pendingID+"/interlock", map[string]any{
		"verdict":  "clear",
		"verifier": "safety-wu",
		"note":     "事实提交后才复核",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(lateReview, http.StatusConflict, "reject clear review past the window"); err != nil {
		return err
	}
	if code, _ := stringField(lateReview.Body, "code"); code != "adjustment_window_exceeded" {
		return fmt.Errorf("late review code: expected adjustment_window_exceeded, got %q", code)
	}

	pendingResult, err := client.get("/commands/" + pendingID)
	if err != nil {
		return err
	}
	if err := requireStatus(pendingResult, http.StatusOK, "read late-reviewed command"); err != nil {
		return err
	}
	pendingView, err := objectField(pendingResult.Body, "command")
	if err != nil {
		return err
	}
	if status, _ := stringField(pendingView, "status"); status != "pending" {
		return fmt.Errorf("window-rejected review must leave command pending, got %q", status)
	}
	if version, _ := floatField(pendingView, "version"); version != 1 {
		return fmt.Errorf("window-rejected review must not advance version, got %v", version)
	}
	if tokenValue, exists := pendingView["execution_token"]; exists && tokenValue != "" {
		return fmt.Errorf("window-rejected review leaked an execution token: %v", tokenValue)
	}

	// A blocked verdict on the same pending command is still processable:
	// blocked reviews never need window capacity and never reserve the gate.
	blocked, err := client.post("/commands/"+pendingID+"/interlock", map[string]any{
		"verdict":  "blocked",
		"verifier": "safety-wu",
		"note":     "窗口已满，安全否决",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(blocked, http.StatusOK, "blocked review still applies"); err != nil {
		return err
	}

	// Gates without a configured window keep the original behavior: the
	// explainable projection simply reports disabled.
	if _, err := fixtureGate(client, "gate-legacy", "无窗口闸门", 5, 0.5); err != nil {
		return err
	}
	legacyWindow, err := getWindow(client, "gate-legacy")
	if err != nil {
		return err
	}
	if enabled, _ := boolField(legacyWindow, "enabled"); enabled {
		return fmt.Errorf("legacy gate window should be disabled")
	}

	fmt.Println("adjustment-window: effective-fact window enforced at request and review with explainable release")
	return nil
}

func fixtureGateWithWindow(
	client *apiClient,
	id string,
	name string,
	maxAperture float64,
	initialAperture float64,
	windowSeconds int64,
	maxWindowAdjustment float64,
) (map[string]any, error) {
	result, err := client.post("/gates", map[string]any{
		"id":                        id,
		"name":                      name,
		"max_aperture":              maxAperture,
		"initial_aperture":          initialAperture,
		"adjustment_window_seconds": windowSeconds,
		"max_window_adjustment":     maxWindowAdjustment,
	})
	if err != nil {
		return nil, err
	}
	if err := requireStatus(result, http.StatusCreated, "create windowed gate"); err != nil {
		return nil, err
	}
	return objectField(result.Body, "gate")
}

func getGate(client *apiClient, id string) (map[string]any, error) {
	result, err := client.get("/gates/" + id)
	if err != nil {
		return nil, err
	}
	if err := requireStatus(result, http.StatusOK, "read gate"); err != nil {
		return nil, err
	}
	return objectField(result.Body, "gate")
}

func getWindow(client *apiClient, gateID string) (map[string]any, error) {
	result, err := client.get("/gates/" + gateID + "/adjustment-window")
	if err != nil {
		return nil, err
	}
	if err := requireStatus(result, http.StatusOK, "read adjustment window"); err != nil {
		return nil, err
	}
	return objectField(result.Body, "adjustment_window")
}

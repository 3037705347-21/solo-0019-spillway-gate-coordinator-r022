package main

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

func runAdjustmentWindow(client *apiClient) error {
	if err := checkRequestTimeRejection(client); err != nil {
		return err
	}
	if err := checkReviewTimeRejection(client); err != nil {
		return err
	}
	if err := checkWindowRelease(client); err != nil {
		return err
	}
	if err := checkAbortedFactCounts(client); err != nil {
		return err
	}
	if err := checkReviewExecuteRace(client); err != nil {
		return err
	}
	fmt.Println("adjustment-window: window admissions, rejections, release, history and race safety verified")
	return nil
}

func windowGate(
	client *apiClient,
	id string,
	maxAperture float64,
	initialAperture float64,
	windowSeconds int64,
	limit float64,
) error {
	result, err := client.post("/gates", map[string]any{
		"id":                        id,
		"name":                      "窗口验证闸 " + id,
		"max_aperture":              maxAperture,
		"initial_aperture":          initialAperture,
		"adjustment_window_seconds": windowSeconds,
		"adjustment_window_limit":   limit,
	})
	if err != nil {
		return err
	}
	return requireStatus(result, http.StatusCreated, "create window gate")
}

func readWindow(client *apiClient, gateID string) (apiResult, error) {
	result, err := client.get("/gates/" + gateID + "/adjustment-window")
	if err != nil {
		return apiResult{}, err
	}
	if err := requireStatus(result, http.StatusOK, "read adjustment window"); err != nil {
		return apiResult{}, err
	}
	return result, nil
}

func windowObject(result apiResult) (map[string]any, error) {
	return objectField(result.Body, "adjustment_window")
}

func completeAdjustment(client *apiClient, gateID string, target float64, reason string) error {
	command, err := fixtureCommand(client, gateID, target, reason, "dispatcher-qiao")
	if err != nil {
		return err
	}
	commandID, err := stringField(command, "id")
	if err != nil {
		return err
	}
	approved, err := fixtureClearReview(client, commandID, "safety-feng")
	if err != nil {
		return err
	}
	token, err := stringField(approved, "execution_token")
	if err != nil {
		return err
	}
	executed, err := client.post("/commands/"+commandID+"/execute", map[string]any{
		"operator":       "operator-shen",
		"token":          token,
		"simulate_fault": false,
	})
	if err != nil {
		return err
	}
	return requireStatus(executed, http.StatusOK, "execute window command")
}

// checkRequestTimeRejection proves the cumulative cap at request time, the
// exact-limit legal boundary, and the explainable effective history.
func checkRequestTimeRejection(client *apiClient) error {
	const gateID = "gate-window-limit"
	if err := windowGate(client, gateID, 4, 0.5, 60, 1.0); err != nil {
		return err
	}

	gateResult, err := client.get("/gates/" + gateID)
	if err != nil {
		return err
	}
	gate, err := objectField(gateResult.Body, "gate")
	if err != nil {
		return err
	}
	seconds, err := floatField(gate, "adjustment_window_seconds")
	if err != nil {
		return err
	}
	if seconds != 60 {
		return fmt.Errorf("gate window seconds: expected 60, got %v", seconds)
	}

	result, err := readWindow(client, gateID)
	if err != nil {
		return err
	}
	window, err := windowObject(result)
	if err != nil {
		return err
	}
	if enabled, _ := window["enabled"].(bool); !enabled {
		return fmt.Errorf("new window gate should report enabled=true")
	}
	if used, _ := window["used"].(float64); used != 0 {
		return fmt.Errorf("fresh window used: expected 0, got %v", used)
	}
	if remaining, _ := window["remaining"].(float64); remaining != 1.0 {
		return fmt.Errorf("fresh window remaining: expected 1, got %v", remaining)
	}

	// First move: 0.5 -> 1.25 = 0.75 travel.
	if err := completeAdjustment(client, gateID, 1.25, "窗口内首次调整"); err != nil {
		return err
	}
	result, err = readWindow(client, gateID)
	if err != nil {
		return err
	}
	window, err = windowObject(result)
	if err != nil {
		return err
	}
	if used, _ := window["used"].(float64); used != 0.75 {
		return fmt.Errorf("used after first move: expected 0.75, got %v", used)
	}
	effective, err := objectSlice(window, "effective_facts")
	if err != nil {
		return err
	}
	if len(effective) != 1 {
		return fmt.Errorf("effective facts: expected 1, got %d", len(effective))
	}
	if outcome, _ := effective[0]["outcome"].(string); outcome != "completed" {
		return fmt.Errorf("fact outcome: expected completed, got %q", outcome)
	}
	if amount, _ := effective[0]["amount"].(float64); amount != 0.75 {
		return fmt.Errorf("fact amount: expected 0.75, got %v", amount)
	}

	// 1.25 -> 2.25 = 1.0 more travel: 0.75 + 1.0 > 1.0, rejected at request.
	overLimit, err := client.post("/commands", map[string]any{
		"gate_id":         gateID,
		"target_aperture": 2.25,
		"reason":          "超额调整请求",
		"requested_by":    "dispatcher-qiao",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(overLimit, http.StatusConflict, "reject request-time over-window command"); err != nil {
		return err
	}
	if code, _ := stringField(overLimit.Body, "code"); code != "adjustment_window_exceeded" {
		return fmt.Errorf("over-window error code: expected adjustment_window_exceeded, got %q", code)
	}
	// A rejected request must not reserve the gate.
	gateResult, err = client.get("/gates/" + gateID)
	if err != nil {
		return err
	}
	gate, err = objectField(gateResult.Body, "gate")
	if err != nil {
		return err
	}
	if status, _ := stringField(gate, "status"); status != "idle" {
		return fmt.Errorf("gate after rejected request: expected idle, got %q", status)
	}
	if active, _ := gate["active_command_id"].(string); active != "" {
		return fmt.Errorf("rejected request must not occupy the gate, active_command_id=%q", active)
	}

	// Exact boundary: 1.25 -> 1.5 = 0.25, 0.75 + 0.25 == limit must be legal.
	if err := completeAdjustment(client, gateID, 1.5, "窗口边界精确等于上限"); err != nil {
		return err
	}
	result, err = readWindow(client, gateID)
	if err != nil {
		return err
	}
	window, err = windowObject(result)
	if err != nil {
		return err
	}
	if used, _ := window["used"].(float64); used != 1.0 {
		return fmt.Errorf("used at boundary: expected 1.0, got %v", used)
	}
	if remaining, _ := window["remaining"].(float64); remaining != 0 {
		return fmt.Errorf("remaining at boundary: expected 0, got %v", remaining)
	}

	// Window full: even a tiny move is rejected.
	tiny, err := client.post("/commands", map[string]any{
		"gate_id":         gateID,
		"target_aperture": 1.6,
		"reason":          "满窗后微调",
		"requested_by":    "dispatcher-qiao",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(tiny, http.StatusConflict, "reject move when window is full"); err != nil {
		return err
	}
	if code, _ := stringField(tiny.Body, "code"); code != "adjustment_window_exceeded" {
		return fmt.Errorf("full-window error code: expected adjustment_window_exceeded, got %q", code)
	}
	return nil
}

// checkReviewTimeRejection proves a pending command created while the window
// had room is still re-evaluated at review time against effective facts, and
// that such a rejection mints no token, bumps no version and occupies nothing.
func checkReviewTimeRejection(client *apiClient) error {
	const gateID = "gate-window-review"
	if err := windowGate(client, gateID, 5, 0, 60, 1.0); err != nil {
		return err
	}

	// Pending command C: 0 -> 0.2 (0.2 travel).
	pending, err := fixtureCommand(client, gateID, 0.2, "先申请待复核", "dispatcher-qiao")
	if err != nil {
		return err
	}
	pendingID, err := stringField(pending, "id")
	if err != nil {
		return err
	}

	// Command D fills the window with real effective travel: 0 -> 1.0.
	if err := completeAdjustment(client, gateID, 1.0, "填满调整窗口"); err != nil {
		return err
	}

	// Clearing C now must be rejected at review time: used 1.0 plus a real
	// 0.8 move (1.0 -> 0.2) exceeds 1.0.
	rejectedReview, err := client.post("/commands/"+pendingID+"/interlock", map[string]any{
		"verdict":  "clear",
		"verifier": "safety-feng",
		"note":     "窗口满后复核",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(rejectedReview, http.StatusConflict, "reject over-window clear review"); err != nil {
		return err
	}
	if code, _ := stringField(rejectedReview.Body, "code"); code != "adjustment_window_exceeded" {
		return fmt.Errorf("review over-window code: expected adjustment_window_exceeded, got %q", code)
	}

	// Command stays pending at version 1 and carries no token.
	commandResult, err := client.get("/commands/" + pendingID)
	if err != nil {
		return err
	}
	command, err := objectField(commandResult.Body, "command")
	if err != nil {
		return err
	}
	if status, _ := stringField(command, "status"); status != "pending" {
		return fmt.Errorf("rejected review must leave command pending, got %q", status)
	}
	if version, _ := command["version"].(float64); version != 1 {
		return fmt.Errorf("rejected review must not bump version, got %v", version)
	}
	if token, exists := command["execution_token"]; exists && token != "" {
		return fmt.Errorf("rejected review must not issue a token, got %v", token)
	}

	// Gate was not reserved by the rejected review.
	gateResult, err := client.get("/gates/" + gateID)
	if err != nil {
		return err
	}
	gate, err := objectField(gateResult.Body, "gate")
	if err != nil {
		return err
	}
	if status, _ := stringField(gate, "status"); status != "idle" {
		return fmt.Errorf("gate after rejected review: expected idle, got %q", status)
	}
	if active, _ := gate["active_command_id"].(string); active != "" {
		return fmt.Errorf("rejected review must not occupy the gate, active_command_id=%q", active)
	}

	// A blocked decision does not reserve or consume window capacity and
	// still takes the command to its rejected terminal state.
	blocked, err := client.post("/commands/"+pendingID+"/interlock", map[string]any{
		"verdict":  "blocked",
		"verifier": "safety-feng",
		"note":     "窗口已满，阻断该申请",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(blocked, http.StatusOK, "blocked review still allowed on pending command"); err != nil {
		return err
	}
	blockedCommand, err := objectField(blocked.Body, "command")
	if err != nil {
		return err
	}
	if status, _ := stringField(blockedCommand, "status"); status != "rejected" {
		return fmt.Errorf("blocked review status: expected rejected, got %q", status)
	}
	if version, _ := blockedCommand["version"].(float64); version != 2 {
		return fmt.Errorf("blocked review version: expected 2, got %v", version)
	}
	if token, exists := blockedCommand["execution_token"]; exists && token != "" {
		return fmt.Errorf("blocked review must never issue a token, got %v", token)
	}
	return nil
}

// checkWindowRelease proves capacity really returns once facts age out of the
// sliding window, and that the aged-out facts remain visible as history.
func checkWindowRelease(client *apiClient) error {
	const gateID = "gate-window-release"
	if err := windowGate(client, gateID, 4, 0, 1, 0.5); err != nil {
		return err
	}

	if err := completeAdjustment(client, gateID, 0.5, "短窗口内调整"); err != nil {
		return err
	}
	blocked, err := client.post("/commands", map[string]any{
		"gate_id":         gateID,
		"target_aperture": 0.6,
		"reason":          "窗口未释放",
		"requested_by":    "dispatcher-qiao",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(blocked, http.StatusConflict, "reject move before window release"); err != nil {
		return err
	}

	time.Sleep(1100 * time.Millisecond)

	result, err := readWindow(client, gateID)
	if err != nil {
		return err
	}
	window, err := windowObject(result)
	if err != nil {
		return err
	}
	if used, _ := window["used"].(float64); used != 0 {
		return fmt.Errorf("released window used: expected 0, got %v", used)
	}
	if remaining, _ := window["remaining"].(float64); remaining != 0.5 {
		return fmt.Errorf("released window remaining: expected 0.5, got %v", remaining)
	}
	effective, err := objectSlice(window, "effective_facts")
	if err != nil {
		return err
	}
	released, err := objectSlice(window, "released_facts")
	if err != nil {
		return err
	}
	if len(effective) != 0 || len(released) != 1 {
		return fmt.Errorf("release partition: expected 0 effective / 1 released, got %d / %d", len(effective), len(released))
	}
	if amount, _ := released[0]["amount"].(float64); amount != 0.5 {
		return fmt.Errorf("released fact amount: expected 0.5, got %v", amount)
	}

	// The same move that was refused a moment earlier is now admitted.
	if err := completeAdjustment(client, gateID, 0.6, "窗口释放后调整"); err != nil {
		return err
	}
	return nil
}

// checkAbortedFactCounts proves an aborted (faulted) execution still records
// real physical travel as an effective fact.
func checkAbortedFactCounts(client *apiClient) error {
	const gateID = "gate-window-fault"
	if err := windowGate(client, gateID, 4, 0, 60, 1.0); err != nil {
		return err
	}

	command, err := fixtureCommand(client, gateID, 0.6, "故障执行", "dispatcher-qiao")
	if err != nil {
		return err
	}
	commandID, err := stringField(command, "id")
	if err != nil {
		return err
	}
	approved, err := fixtureClearReview(client, commandID, "safety-feng")
	if err != nil {
		return err
	}
	token, err := stringField(approved, "execution_token")
	if err != nil {
		return err
	}
	executed, err := client.post("/commands/"+commandID+"/execute", map[string]any{
		"operator":       "operator-shen",
		"token":          token,
		"simulate_fault": true,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(executed, http.StatusOK, "execute faulting command"); err != nil {
		return err
	}

	result, err := readWindow(client, gateID)
	if err != nil {
		return err
	}
	window, err := windowObject(result)
	if err != nil {
		return err
	}
	if used, _ := window["used"].(float64); used != 0.6 {
		return fmt.Errorf("aborted fact used: expected 0.6, got %v", used)
	}
	effective, err := objectSlice(window, "effective_facts")
	if err != nil {
		return err
	}
	if len(effective) != 1 {
		return fmt.Errorf("aborted fact count: expected 1, got %d", len(effective))
	}
	if outcome, _ := effective[0]["outcome"].(string); outcome != "aborted" {
		return fmt.Errorf("fault fact outcome: expected aborted, got %q", outcome)
	}
	return nil
}

// checkReviewExecuteRace races a window-admission review against an
// execution that commits effective travel. Physical cumulative travel must
// never exceed the limit regardless of scheduling: either the second review
// loses the reservation, or it is admitted only at the exact boundary.
func checkReviewExecuteRace(client *apiClient) error {
	for iteration := 0; iteration < 6; iteration++ {
		gateID := fmt.Sprintf("gate-window-race-%d", iteration)
		if err := windowGate(client, gateID, 10, 0, 60, 1.0); err != nil {
			return err
		}

		first, err := fixtureCommand(client, gateID, 0.5, "并发甲", "dispatcher-qiao")
		if err != nil {
			return err
		}
		second, err := fixtureCommand(client, gateID, 1.0, "并发乙", "dispatcher-qiao")
		if err != nil {
			return err
		}
		firstID, _ := stringField(first, "id")
		secondID, _ := stringField(second, "id")

		approved, err := fixtureClearReview(client, firstID, "safety-feng")
		if err != nil {
			return err
		}
		firstToken, _ := stringField(approved, "execution_token")

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			// Executing the first command commits 0.5 of effective travel
			// and releases the gate.
			defer wg.Done()
			_, _ = client.post("/commands/"+firstID+"/execute", map[string]any{
				"operator":       "operator-shen",
				"token":          firstToken,
				"simulate_fault": false,
			})
		}()
		go func() {
			// Clearing the second command either loses the reservation
			// (gate still reserved) or must be re-admitted against the
			// freshly committed travel: 0.5 used + 0.5 candidate == limit.
			defer wg.Done()
			review, reviewErr := client.post("/commands/"+secondID+"/interlock", map[string]any{
				"verdict":  "clear",
				"verifier": "safety-feng",
				"note":     "并发复核",
			})
			if reviewErr != nil || review.Status != http.StatusOK {
				return
			}
			approvedCommand, objErr := objectField(review.Body, "command")
			if objErr != nil {
				return
			}
			token, tokenErr := stringField(approvedCommand, "execution_token")
			if tokenErr != nil {
				return
			}
			_, _ = client.post("/commands/"+secondID+"/execute", map[string]any{
				"operator":       "operator-shen",
				"token":          token,
				"simulate_fault": false,
			})
		}()
		wg.Wait()

		result, err := readWindow(client, gateID)
		if err != nil {
			return err
		}
		window, err := windowObject(result)
		if err != nil {
			return err
		}
		used, _ := window["used"].(float64)
		// Only two legal outcomes: the second review lost (0.5) or it was
		// admitted exactly on the boundary (1.0). Anything else is an
		// overrun caused by the race.
		if used != 0.5 && !(used > 1.0-1e-9 && used < 1.0+1e-9) {
			return fmt.Errorf("race iteration %d produced illegal cumulative travel: used=%v", iteration, used)
		}

		gateResult, err := client.get("/gates/" + gateID)
		if err != nil {
			return err
		}
		gate, err := objectField(gateResult.Body, "gate")
		if err != nil {
			return err
		}
		if status, _ := stringField(gate, "status"); status != "idle" {
			return fmt.Errorf("race iteration %d left gate in status %q", iteration, status)
		}
		aperture, _ := floatField(gate, "aperture")
		if aperture != 0.5 && !(aperture > 1.0-1e-9 && aperture < 1.0+1e-9) {
			return fmt.Errorf("race iteration %d settled at illegal aperture %v", iteration, aperture)
		}
		if active, _ := gate["active_command_id"].(string); active != "" {
			return fmt.Errorf("race iteration %d left an active command reference", iteration)
		}
	}
	return nil
}

func objectSlice(object map[string]any, name string) ([]map[string]any, error) {
	value, exists := object[name]
	if !exists {
		return nil, fmt.Errorf("object is missing array field %q", name)
	}
	raw, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("object field %q is not an array", name)
	}
	items := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("object field %q contains a non-object entry", name)
		}
		items = append(items, entry)
	}
	return items, nil
}

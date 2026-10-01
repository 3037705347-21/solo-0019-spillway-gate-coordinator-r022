package storage

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
)

var testEpoch = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: testEpoch}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestRepository() (*MemoryRepository, *testClock) {
	clock := newTestClock()
	return NewMemoryRepositoryWithClock(clock.Now), clock
}

func newWindowedGate(t *testing.T, repo *MemoryRepository, id string, initial, maxAperture float64, windowSeconds int64, maxAdjustment float64) domain.Gate {
	t.Helper()
	gate, err := domain.NewGate(id, "测试闸门", maxAperture, initial, windowSeconds, maxAdjustment, testEpoch)
	if err != nil {
		t.Fatalf("new gate: %v", err)
	}
	if err := repo.CreateGate(context.Background(), gate); err != nil {
		t.Fatalf("create gate: %v", err)
	}
	return gate
}

func mustCreateCommand(t *testing.T, repo *MemoryRepository, gate domain.Gate, id string, target float64) domain.GateCommand {
	t.Helper()
	command, err := domain.NewGateCommand(id, gate.ID, target, gate.Aperture, gate.MaxAperture, "测试调整", "dispatcher-1", testEpoch)
	if err != nil {
		t.Fatalf("new command %s: %v", id, err)
	}
	if err := repo.CreateCommand(context.Background(), command); err != nil {
		t.Fatalf("create command %s: %v", id, err)
	}
	return command
}

func staticToken(value string) (func() (string, error), *int) {
	calls := 0
	return func() (string, error) {
		calls++
		return value, nil
	}, &calls
}

func reviewClear(t *testing.T, repo *MemoryRepository, commandID, token string) (domain.GateCommand, domain.Gate) {
	t.Helper()
	mint, _ := staticToken(token)
	command, gate, err := repo.ReviewCommand(
		context.Background(),
		commandID,
		domain.InterlockDecision{Verdict: domain.InterlockClear, Verifier: "safety-1"},
		mint,
	)
	if err != nil {
		t.Fatalf("review %s clear: %v", commandID, err)
	}
	return command, gate
}

func executeCommand(t *testing.T, repo *MemoryRepository, commandID, token string, fault bool) (domain.GateCommand, domain.Gate) {
	t.Helper()
	command, gate, err := repo.ExecuteCommand(context.Background(), commandID, token, "operator-1", fault)
	if err != nil {
		t.Fatalf("execute %s: %v", commandID, err)
	}
	return command, gate
}

// A request the window rejects must never be stored: no pending occupation,
// and the gate is untouched (same version).
func TestCreateCommandRejectedByWindowOccupiesNothing(t *testing.T) {
	repo, _ := newTestRepository()
	gate := newWindowedGate(t, repo, "gate-a", 0, 5, 3600, 1.0)

	first := mustCreateCommand(t, repo, gate, "cmd-1", 0.6)
	reviewClear(t, repo, first.ID, "tok-1")
	completed, settledGate := executeCommand(t, repo, first.ID, "tok-1", false)
	if completed.Status != domain.CommandCompleted || settledGate.Aperture != 0.6 {
		t.Fatalf("setup execution mismatch: %s %v", completed.Status, settledGate.Aperture)
	}

	oversized, err := domain.NewGateCommand(
		"cmd-over", gate.ID, 1.1, settledGate.Aperture, gate.MaxAperture, "超额申请", "dispatcher-1", testEpoch,
	)
	if err != nil {
		t.Fatalf("build oversized command: %v", err)
	}
	err = repo.CreateCommand(context.Background(), oversized)
	if err == nil || domain.ErrorCodeOf(err) != "adjustment_window_exceeded" {
		t.Fatalf("expected adjustment_window_exceeded, got %v", err)
	}

	if _, getErr := repo.Command(context.Background(), "cmd-over"); domain.ErrorCodeOf(getErr) != "command_not_found" {
		t.Fatalf("rejected command must not be stored, lookup error: %v", getErr)
	}

	after, err := repo.Gate(context.Background(), gate.ID)
	if err != nil {
		t.Fatalf("read gate: %v", err)
	}
	if after.Version != settledGate.Version || after.ActiveCommandID != "" || after.Status != domain.GateIdle {
		t.Fatalf("rejected request mutated the gate: %+v", after)
	}
}

// A clear review the window rejects must leave the command pending, issue no
// token, not reserve the gate and not advance any version.
func TestClearReviewRejectedByWindowIssuesNothing(t *testing.T) {
	repo, _ := newTestRepository()
	gate := newWindowedGate(t, repo, "gate-b", 0, 5, 3600, 1.0)

	first := mustCreateCommand(t, repo, gate, "cmd-1", 0.6)
	pendingSecond := mustCreateCommand(t, repo, gate, "cmd-2", 0.1)
	reviewClear(t, repo, first.ID, "tok-1")
	executeCommand(t, repo, first.ID, "tok-1", false)

	gateMid, err := repo.Gate(context.Background(), gate.ID)
	if err != nil {
		t.Fatalf("read gate: %v", err)
	}
	gateVersionBefore := gateMid.Version

	mint, mintCalls := staticToken("must-not-be-used")
	_, _, err = repo.ReviewCommand(
		context.Background(),
		pendingSecond.ID,
		domain.InterlockDecision{Verdict: domain.InterlockClear, Verifier: "safety-1"},
		mint,
	)
	if err == nil || domain.ErrorCodeOf(err) != "adjustment_window_exceeded" {
		t.Fatalf("expected window rejection, got %v", err)
	}
	if *mintCalls != 0 {
		t.Fatal("no execution token may be minted for a window-rejected review")
	}

	stored, err := repo.Command(context.Background(), pendingSecond.ID)
	if err != nil {
		t.Fatalf("read pending command: %v", err)
	}
	if stored.Status != domain.CommandPending || stored.Version != 1 {
		t.Fatalf("rejected review must not transition the command: status=%s version=%d", stored.Status, stored.Version)
	}
	if stored.ExecutionToken != "" || stored.Interlock != nil {
		t.Fatal("rejected review must not attach a token or interlock decision")
	}
	gateAfter, err := repo.Gate(context.Background(), gate.ID)
	if err != nil {
		t.Fatalf("read gate: %v", err)
	}
	if gateAfter.Version != gateVersionBefore || gateAfter.Status != domain.GateIdle || gateAfter.ActiveCommandID != "" {
		t.Fatalf("rejected review must not reserve or version the gate: %+v", gateAfter)
	}
}

// The window releases budget only through aging facts: once the prior
// execution leaves the trailing window, the same pending clear review is
// admitted and gets its token.
func TestWindowBudgetIsReleasedAsHistoryAgesOut(t *testing.T) {
	repo, clock := newTestRepository()
	gate := newWindowedGate(t, repo, "gate-c", 0, 5, 60, 1.0)

	first := mustCreateCommand(t, repo, gate, "cmd-1", 0.6)
	mustCreateCommand(t, repo, gate, "cmd-2", 0.1)
	reviewClear(t, repo, first.ID, "tok-1")
	executeCommand(t, repo, first.ID, "tok-1", false)

	clock.Advance(61 * time.Second)
	mint, calls := staticToken("tok-2")
	approved, reserved, err := repo.ReviewCommand(
		context.Background(),
		"cmd-2",
		domain.InterlockDecision{Verdict: domain.InterlockClear, Verifier: "safety-1"},
		mint,
	)
	if err != nil {
		t.Fatalf("aged-out fact must release the window, got %v", err)
	}
	if approved.Status != domain.CommandApproved || approved.ExecutionToken != "tok-2" || *calls != 1 {
		t.Fatalf("released review must issue exactly one token: %+v calls=%d", approved, *calls)
	}
	if reserved.Status != domain.GateReserved || reserved.ActiveCommandID != "cmd-2" {
		t.Fatalf("approved review must reserve the gate: %+v", reserved)
	}

	_, snapshot, err := repo.AdjustmentWindow(context.Background(), gate.ID)
	if err != nil {
		t.Fatalf("read window: %v", err)
	}
	if snapshot.Used != 0 || snapshot.Available != 1.0 || len(snapshot.Facts) != 0 {
		t.Fatalf("aged-out fact must not count: %+v", snapshot)
	}
}

// A simulated fault moves the gate before aborting; that physical travel is
// an effective fact and must still consume window budget.
func TestAbortedFaultMovementIsAnEffectiveFact(t *testing.T) {
	repo, _ := newTestRepository()
	gate := newWindowedGate(t, repo, "gate-d", 0, 5, 3600, 1.0)

	faulted := mustCreateCommand(t, repo, gate, "cmd-fault", 0.9)
	reviewClear(t, repo, faulted.ID, "tok-f")
	aborted, settled := executeCommand(t, repo, faulted.ID, "tok-f", true)
	if aborted.Status != domain.CommandAborted || settled.Status != domain.GateFaulted {
		t.Fatalf("expected aborted/faulted, got %s/%s", aborted.Status, settled.Status)
	}
	if aborted.Adjustment != 0.9 || aborted.ExecutedAt == nil || aborted.FinalAperture != 0.9 {
		t.Fatalf("faulted command must record its real movement: %+v", aborted)
	}

	_, snapshot, err := repo.AdjustmentWindow(context.Background(), gate.ID)
	if err != nil {
		t.Fatalf("read window: %v", err)
	}
	if snapshot.Used != 0.9 || len(snapshot.Facts) != 1 || snapshot.Facts[0].Status != domain.CommandAborted {
		t.Fatalf("aborted movement must be counted: %+v", snapshot)
	}
}

// Concurrent execution (committing a fact) and clear review must never allow
// an over-limit approval: every post-execution review sees the fresh fact
// because both paths stamp their evaluation instant under the same lock.
func TestConcurrentReviewAndExecutionNeverOverAdmits(t *testing.T) {
	const iterations = 300
	for i := 0; i < iterations; i++ {
		repo, _ := newTestRepository()
		id := fmt.Sprintf("gate-%03d", i)
		gate := newWindowedGate(t, repo, id, 0, 5, 3600, 1.0)

		exec := mustCreateCommand(t, repo, gate, "cmd-exec", 0.6)
		reviewClear(t, repo, exec.ID, "tok-exec")
		// Filed while the gate is at 0; review prospective travel is
		// recomputed from the live gate aperture (0.6 once exec settles),
		// giving 0.6 + 0.55 > 1.0.
		pending := mustCreateCommand(t, repo, gate, "cmd-pending", 0.05)

		var wg sync.WaitGroup
		wg.Add(2)
		var executeErr, reviewErr error
		go func() {
			defer wg.Done()
			_, _, executeErr = repo.ExecuteCommand(
				context.Background(), exec.ID, "tok-exec", "operator-1", false,
			)
		}()
		go func() {
			defer wg.Done()
			mint, _ := staticToken("tok-pending")
			_, _, reviewErr = repo.ReviewCommand(
				context.Background(),
				pending.ID,
				domain.InterlockDecision{Verdict: domain.InterlockClear, Verifier: "safety-1"},
				mint,
			)
		}()
		wg.Wait()

		if executeErr != nil {
			t.Fatalf("iteration %d: execution failed: %v", i, executeErr)
		}
		if reviewErr == nil {
			t.Fatalf("iteration %d: clear review must not win against the live fact", i)
		}
		switch domain.ErrorCodeOf(reviewErr) {
		case "gate_not_idle", "adjustment_window_exceeded":
			// Both are safe: either execution had not released the gate yet
			// (reservation conflict) or it had and the fresh fact rejected.
		default:
			t.Fatalf("iteration %d: unexpected review error %v", i, reviewErr)
		}

		stored, err := repo.Command(context.Background(), pending.ID)
		if err != nil {
			t.Fatalf("iteration %d: read pending: %v", i, err)
		}
		if stored.Status == domain.CommandApproved {
			t.Fatalf("iteration %d: pending command was approved during the race", i)
		}

		// After the race settles, a fresh review must deterministically fail
		// on window grounds (proving the fact is visible), with no token.
		mint, calls := staticToken("tok-late")
		_, _, err = repo.ReviewCommand(
			context.Background(),
			pending.ID,
			domain.InterlockDecision{Verdict: domain.InterlockClear, Verifier: "safety-1"},
			mint,
		)
		if err == nil || domain.ErrorCodeOf(err) != "adjustment_window_exceeded" {
			t.Fatalf("iteration %d: expected window rejection after execution, got %v", i, err)
		}
		if *calls != 0 {
			t.Fatalf("iteration %d: token minted despite rejection", i)
		}

		// Final invariant check: gate idle/aperture 0.6 and exactly one fact.
		finalGate, err := repo.Gate(context.Background(), id)
		if err != nil {
			t.Fatalf("iteration %d: gate read: %v", i, err)
		}
		if finalGate.Status != domain.GateIdle || finalGate.Aperture != 0.6 || finalGate.ActiveCommandID != "" {
			t.Fatalf("iteration %d: gate corrupted by the race: %+v", i, finalGate)
		}
		_, snapshot, err := repo.AdjustmentWindow(context.Background(), id)
		if err != nil {
			t.Fatalf("iteration %d: window read: %v", i, err)
		}
		if snapshot.Used != 0.6 || len(snapshot.Facts) != 1 || snapshot.Available != 0.4 {
			t.Fatalf("iteration %d: window corrupted: %+v", i, snapshot)
		}
	}
}

func TestAdjustmentWindowReadOnlyProjection(t *testing.T) {
	repo, _ := newTestRepository()
	gate := newWindowedGate(t, repo, "gate-e", 1.0, 5, 60, 2.0)

	c1 := mustCreateCommand(t, repo, gate, "cmd-1", 1.5)
	reviewClear(t, repo, c1.ID, "tok-1")
	completed, _ := executeCommand(t, repo, c1.ID, "tok-1", false)

	_, snapshot, err := repo.AdjustmentWindow(context.Background(), gate.ID)
	if err != nil {
		t.Fatalf("window read: %v", err)
	}
	if !snapshot.Enabled || snapshot.Used != 0.5 || snapshot.Available != 1.5 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if len(snapshot.Facts) != 1 {
		t.Fatalf("expected one explainable fact, got %+v", snapshot.Facts)
	}
	fact := snapshot.Facts[0]
	if fact.CommandID != completed.ID || fact.Status != domain.CommandCompleted ||
		fact.StartingAperture != 1.0 || fact.FinalAperture != 1.5 || fact.Adjustment != 0.5 {
		t.Fatalf("fact is not explainable: %+v", fact)
	}

	if _, _, err := repo.AdjustmentWindow(context.Background(), "missing"); domain.ErrorCodeOf(err) != "gate_not_found" {
		t.Fatalf("expected gate_not_found, got %v", err)
	}
}

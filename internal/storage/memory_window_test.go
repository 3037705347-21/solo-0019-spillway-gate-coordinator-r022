package storage

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
)

var suffixCounter int64

func randomSuffix() string {
	n := atomic.AddInt64(&suffixCounter, 1)
	return time.Now().Format("010203150405.000000000") + "-" + strconv.FormatInt(n, 10)
}

func newWindowedGate(t *testing.T, repo *MemoryRepository, id string, windowSeconds int64, limit float64) domain.Gate {
	t.Helper()
	gate, err := domain.NewGate(id, "window gate", 10, 0, windowSeconds, limit, time.Now())
	if err != nil {
		t.Fatalf("new gate: %v", err)
	}
	if err := repo.CreateGate(context.Background(), gate); err != nil {
		t.Fatalf("create gate: %v", err)
	}
	return gate
}

func mustReloadGate(t *testing.T, repo *MemoryRepository, id string) domain.Gate {
	t.Helper()
	gate, err := repo.Gate(context.Background(), id)
	if err != nil {
		t.Fatalf("reload gate %s: %v", id, err)
	}
	return gate
}

func approxEqual(a, b float64) bool {
	delta := a - b
	if delta < 0 {
		delta = -delta
	}
	return delta < 1e-9
}

func newPendingCommand(t *testing.T, repo *MemoryRepository, gate domain.Gate, target float64) domain.GateCommand {
	t.Helper()
	command, err := domain.NewGateCommand(
		"cmd-"+gate.ID+"-"+randomSuffix(),
		gate.ID,
		target,
		gate.Aperture,
		gate.MaxAperture,
		"test reason",
		"dispatcher",
		time.Now(),
	)
	if err != nil {
		t.Fatalf("new command: %v", err)
	}
	if err := repo.CreateCommand(context.Background(), command, time.Now()); err != nil {
		t.Fatalf("create command: %v", err)
	}
	return command
}

func clearReview(
	t *testing.T,
	repo *MemoryRepository,
	commandID string,
) (domain.GateCommand, domain.Gate) {
	t.Helper()
	decision, err := domain.NewInterlockDecision("clear", "safety-wu", "", time.Now())
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	command, gate, err := repo.ReviewCommand(
		context.Background(),
		commandID,
		decision,
		func() (string, error) { return "token-" + randomSuffix(), nil },
		time.Now(),
	)
	if err != nil {
		t.Fatalf("clear review %s: %v", commandID, err)
	}
	return command, gate
}

func reviewMustFail(
	t *testing.T,
	repo *MemoryRepository,
	commandID string,
	code string,
) {
	t.Helper()
	decision, err := domain.NewInterlockDecision("clear", "safety-wu", "", time.Now())
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	issued := false
	_, _, err = repo.ReviewCommand(
		context.Background(),
		commandID,
		decision,
		func() (string, error) {
			issued = true
			return "token-should-not-issue", nil
		},
		time.Now(),
	)
	if err == nil {
		t.Fatalf("review of %s unexpectedly succeeded", commandID)
	}
	if domain.ErrorCodeOf(err) != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
	if issued {
		t.Fatalf("a rejected review must not issue a credential")
	}
}

// TestReviewExecuteRaceCannotOverrunWindow hammers the exact race the task
// calls out: window computation at review time versus an execution committing
// a new effective fact. Cumulative effective travel can never pass the limit.
func TestReviewExecuteRaceCannotOverrunWindow(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		repo := NewMemoryRepository()
		gate := newWindowedGate(t, repo, "race", 3600, 1.0)

		first := newPendingCommand(t, repo, gate, 0.5)
		second := newPendingCommand(t, repo, gate, 1.0)
		approved, _ := clearReview(t, repo, first.ID)

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _, _ = repo.ExecuteCommand(
				context.Background(),
				first.ID,
				approved.ExecutionToken,
				"operator",
				false,
				time.Now(),
			)
		}()
		go func() {
			defer wg.Done()
			<-start
			decision, _ := domain.NewInterlockDecision("clear", "safety-wu", "", time.Now())
			_, _, _ = repo.ReviewCommand(
				context.Background(),
				second.ID,
				decision,
				func() (string, error) { return "token-second", nil },
				time.Now(),
			)
		}()
		close(start)
		wg.Wait()

		stored, err := repo.Command(context.Background(), second.ID)
		if err != nil {
			t.Fatalf("load second command: %v", err)
		}

		finalGate, facts, err := repo.AdjustmentWindow(context.Background(), gate.ID)
		if err != nil {
			t.Fatalf("load window: %v", err)
		}
		used := domain.SumAdjustmentAmounts(facts)
		if used > 1.0+1e-9 {
			t.Fatalf("iteration %d overrun the window: used=%v", iteration, used)
		}
		// If the second review was admitted, executing it must keep the
		// total at the exact boundary; otherwise it must still be pending
		// with no token and an untouched version.
		if stored.Status == domain.CommandApproved {
			_, _, err := repo.ExecuteCommand(
				context.Background(),
				second.ID,
				stored.ExecutionToken,
				"operator",
				false,
				time.Now(),
			)
			if err != nil {
				t.Fatalf("iteration %d execute approved second: %v", iteration, err)
			}
			finalGate, facts, _ = repo.AdjustmentWindow(context.Background(), gate.ID)
			used = domain.SumAdjustmentAmounts(facts)
			if used < 1.0-1e-9 || used > 1.0+1e-9 {
				t.Fatalf("iteration %d expected boundary total 1.0, got %v", iteration, used)
			}
		} else if stored.Status != domain.CommandPending || stored.ExecutionToken != "" || stored.Version != 1 {
			t.Fatalf("iteration %d losing review must leave a pristine pending command, got %#v", iteration, stored)
		}
		if finalGate.Status != domain.GateIdle || finalGate.ActiveCommandID != "" {
			t.Fatalf("iteration %d gate not cleanly settled: %#v", iteration, finalGate)
		}
	}
}

// TestOverWindowRequestIsRejectedWithoutStorage proves a request-time
// rejection never places an occupation or a command record.
func TestOverWindowRequestIsRejectedWithoutStorage(t *testing.T) {
	repo := NewMemoryRepository()
	gate := newWindowedGate(t, repo, "req", 3600, 0.5)
	first := newPendingCommand(t, repo, gate, 0.5)
	approved, _ := clearReview(t, repo, first.ID)
	if _, _, err := repo.ExecuteCommand(
		context.Background(), first.ID, approved.ExecutionToken, "operator", false, time.Now(),
	); err != nil {
		t.Fatalf("execute first: %v", err)
	}

	over := mustCommand(t, repo, mustReloadGate(t, repo, gate.ID), 0.6)
	err := repo.CreateCommand(context.Background(), over, time.Now())
	if err == nil || domain.ErrorCodeOf(err) != "adjustment_window_exceeded" {
		t.Fatalf("expected adjustment_window_exceeded, got %v", err)
	}
	if _, getErr := repo.Command(context.Background(), over.ID); getErr == nil {
		t.Fatalf("rejected command must not be stored")
	}
	finalGate, _ := repo.Gate(context.Background(), gate.ID)
	if finalGate.Status != domain.GateIdle || finalGate.ActiveCommandID != "" {
		t.Fatalf("gate must stay idle after rejected request, got %#v", finalGate)
	}
}

// TestReviewTimeWindowRejectionKeepsCommandPending proves facts accumulated
// while a command is pending are enforced at the clear review, atomically.
func TestReviewTimeWindowRejectionKeepsCommandPending(t *testing.T) {
	repo := NewMemoryRepository()
	gate := newWindowedGate(t, repo, "rev", 3600, 1.0)

	pending := newPendingCommand(t, repo, gate, 0.2)
	setup := newPendingCommand(t, repo, gate, 1.0)
	approvedSetup, _ := clearReview(t, repo, setup.ID)
	if _, _, err := repo.ExecuteCommand(
		context.Background(), setup.ID, approvedSetup.ExecutionToken, "operator", false, time.Now(),
	); err != nil {
		t.Fatalf("execute setup: %v", err)
	}

	reviewMustFail(t, repo, pending.ID, "adjustment_window_exceeded")

	stored, err := repo.Command(context.Background(), pending.ID)
	if err != nil {
		t.Fatalf("load pending: %v", err)
	}
	if stored.Status != domain.CommandPending || stored.Version != 1 || stored.ExecutionToken != "" {
		t.Fatalf("rejected review must leave a pristine pending command, got %#v", stored)
	}
	finalGate, _ := repo.Gate(context.Background(), gate.ID)
	if finalGate.Status != domain.GateIdle || finalGate.ActiveCommandID != "" {
		t.Fatalf("rejected review must not occupy the gate, got %#v", finalGate)
	}

	// A blocked decision is still allowed and does not consume the window.
	blocked, err := domain.NewInterlockDecision("blocked", "safety-wu", "window full", time.Now())
	if err != nil {
		t.Fatalf("blocked decision: %v", err)
	}
	commandAfter, _, err := repo.ReviewCommand(context.Background(), pending.ID, blocked, nil, time.Now())
	if err != nil {
		t.Fatalf("blocked review: %v", err)
	}
	if commandAfter.Status != domain.CommandRejected || commandAfter.Version != 2 {
		t.Fatalf("blocked review must reject the command, got %#v", commandAfter)
	}
}

// TestExecutionFactsCoverCompletedAndAbortedAndAging proves real travel is
// recorded for both outcomes and slides out of the window to release room.
func TestExecutionFactsCoverCompletedAndAbortedAndAging(t *testing.T) {
	repo := NewMemoryRepository()
	gate := newWindowedGate(t, repo, "facts", 60, 1.0)

	completed := newPendingCommand(t, repo, gate, 0.4)
	approved, _ := clearReview(t, repo, completed.ID)
	if _, _, err := repo.ExecuteCommand(
		context.Background(), completed.ID, approved.ExecutionToken, "operator", false, time.Now(),
	); err != nil {
		t.Fatalf("execute completed: %v", err)
	}

	faultGate := mustReloadGate(t, repo, gate.ID)
	faulted := newPendingCommand(t, repo, faultGate, 0.6)
	approvedFault, _ := clearReview(t, repo, faulted.ID)
	if _, _, err := repo.ExecuteCommand(
		context.Background(), faulted.ID, approvedFault.ExecutionToken, "operator", true, time.Now(),
	); err != nil {
		t.Fatalf("execute faulted: %v", err)
	}

	_, facts, err := repo.AdjustmentWindow(context.Background(), gate.ID)
	if err != nil {
		t.Fatalf("load facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected two effective facts, got %d", len(facts))
	}
	if facts[1].Outcome != domain.AdjustmentAborted {
		t.Fatalf("second fact must be aborted, got %q", facts[1].Outcome)
	}
	if !approxEqual(facts[1].Amount, 0.2) || facts[1].StartingAperture != 0.4 || facts[1].FinalAperture != 0.6 {
		t.Fatalf("aborted fact travel mismatch: %#v", facts[1])
	}
	if used := domain.SumAdjustmentAmounts(facts); !approxEqual(used, 0.6) {
		t.Fatalf("expected 0.6 cumulative travel, got %v", used)
	}

	// After both facts age out the window is empty even though history stays.
	later := time.Now().Add(2 * time.Minute)
	_, aged, err := repo.AdjustmentWindow(context.Background(), gate.ID)
	if err != nil {
		t.Fatalf("reload facts: %v", err)
	}
	effective, released := domain.PartitionAdjustmentFacts(aged, time.Minute, later)
	if len(effective) != 0 || len(released) != 2 {
		t.Fatalf("expected both facts released, got effective=%d released=%d", len(effective), len(released))
	}
}

func mustCommand(t *testing.T, repo *MemoryRepository, gate domain.Gate, target float64) domain.GateCommand {
	t.Helper()
	command, err := domain.NewGateCommand(
		"cmd-"+gate.ID+"-"+randomSuffix(),
		gate.ID,
		target,
		gate.Aperture,
		gate.MaxAperture,
		"test reason",
		"dispatcher",
		time.Now(),
	)
	if err != nil {
		t.Fatalf("new command: %v", err)
	}
	return command
}

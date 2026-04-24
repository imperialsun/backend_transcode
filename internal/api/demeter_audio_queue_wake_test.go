package api

import (
	"context"
	"testing"
	"time"
)

func TestDemeterQueueLaneWakeIsBufferedAndScoped(t *testing.T) {
	manager := &DemeterAudioQueueManager{}

	manager.notifyLaneWorkAvailable(2)

	laneTwoCtx, laneTwoCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer laneTwoCancel()
	if !manager.waitForLaneWorkAvailable(laneTwoCtx, 2, time.Hour) {
		t.Fatal("expected buffered lane 2 wake signal to be consumed")
	}

	laneOneCtx, laneOneCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer laneOneCancel()
	if manager.waitForLaneWorkAvailable(laneOneCtx, 1, time.Hour) {
		t.Fatal("expected lane 1 to remain asleep when only lane 2 was notified")
	}
}

func TestDemeterQueueLaneWakeFallsBack(t *testing.T) {
	manager := &DemeterAudioQueueManager{}

	startedAt := time.Now()
	if !manager.waitForLaneWorkAvailable(context.Background(), 1, 10*time.Millisecond) {
		t.Fatal("expected fallback timer to wake the lane")
	}
	if elapsed := time.Since(startedAt); elapsed < 8*time.Millisecond {
		t.Fatalf("expected fallback wait, got %s", elapsed)
	}
}

func TestDemeterQueueIdleFallbackIsThirtySeconds(t *testing.T) {
	if demeterAudioQueueIdleFallback != 30*time.Second {
		t.Fatalf("unexpected idle fallback: got %s want 30s", demeterAudioQueueIdleFallback)
	}
}

func TestDemeterQueueFinishRetryPauseWakesOpenLanes(t *testing.T) {
	manager := &DemeterAudioQueueManager{
		lanes: map[int]*demeterAudioQueueLaneState{
			1: {ID: 1, Open: true},
			2: {ID: 2, Open: true},
			3: {ID: 3},
		},
	}

	if !manager.startMistralRetryPause(1, "operation-1", 0) {
		t.Fatal("failed to start retry pause")
	}
	if !manager.finishMistralRetryPause(1, "operation-1", 0) {
		t.Fatal("failed to finish retry pause")
	}

	for _, laneID := range []int{1, 2} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		if !manager.waitForLaneWorkAvailable(ctx, laneID, time.Hour) {
			cancel()
			t.Fatalf("expected lane %d to wake after retry pause", laneID)
		}
		cancel()
	}

	closedLaneCtx, closedLaneCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer closedLaneCancel()
	if manager.waitForLaneWorkAvailable(closedLaneCtx, 3, time.Hour) {
		t.Fatal("expected closed lane to remain asleep after retry pause")
	}
}

func TestDemeterQueueRetryPauseWaiterResumesWhenPauseFinishes(t *testing.T) {
	manager := &DemeterAudioQueueManager{}

	if !manager.startMistralRetryPause(1, "operation-1", 0) {
		t.Fatal("failed to start retry pause")
	}

	waiterDone := make(chan bool, 1)
	go func() {
		waiterDone <- manager.waitForMistralRetryPause(context.Background(), 2)
	}()

	select {
	case <-waiterDone:
		t.Fatal("expected non-owner lane to block while retry pause is active")
	case <-time.After(20 * time.Millisecond):
	}

	if !manager.finishMistralRetryPause(1, "operation-1", 0) {
		t.Fatal("failed to finish retry pause")
	}

	select {
	case ok := <-waiterDone:
		if !ok {
			t.Fatal("expected retry pause waiter to resume successfully")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("retry pause waiter did not resume after finish signal")
	}
}

func TestDemeterQueueRetryPauseWaiterStopsWhenContextCancelled(t *testing.T) {
	manager := &DemeterAudioQueueManager{}

	if !manager.startMistralRetryPause(1, "operation-1", 0) {
		t.Fatal("failed to start retry pause")
	}

	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan bool, 1)
	go func() {
		waiterDone <- manager.waitForMistralRetryPause(ctx, 2)
	}()

	select {
	case <-waiterDone:
		t.Fatal("expected non-owner lane to block while retry pause is active")
	case <-time.After(20 * time.Millisecond):
	}

	cancel()

	select {
	case ok := <-waiterDone:
		if ok {
			t.Fatal("expected retry pause waiter to stop after context cancellation")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("retry pause waiter did not stop after context cancellation")
	}
}

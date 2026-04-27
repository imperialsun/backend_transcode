package api

import (
	"context"
	"testing"
	"time"
)

func TestDemeterReportQueueSnapshotChangesAreBroadcast(t *testing.T) {
	manager := &DemeterReportQueueManager{}
	changes, unsubscribe := manager.subscribeSnapshotChanges()
	defer unsubscribe()

	manager.notifySnapshotChanged()

	select {
	case <-changes:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected snapshot change notification")
	}

	unsubscribe()
	manager.notifySnapshotChanged()

	select {
	case <-changes:
		t.Fatal("did not expect notification after unsubscribe")
	default:
	}
}

func TestDemeterReportQueueRetryPauseWaiterResumesWhenPauseFinishes(t *testing.T) {
	manager := &DemeterReportQueueManager{}

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

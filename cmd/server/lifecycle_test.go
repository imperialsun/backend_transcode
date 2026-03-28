package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestRunServerLifecycle_LogsTraceSteps(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	listenStarted := make(chan struct{})
	shutdownCalled := make(chan struct{})
	signalCh := make(chan os.Signal, 1)

	runtime := serverRuntime{
		listen: func() error {
			close(listenStarted)
			<-shutdownCalled
			return nil
		},
		shutdown: func(context.Context) error {
			close(shutdownCalled)
			return nil
		},
		signalCh: signalCh,
	}

	go func() {
		<-listenStarted
		signalCh <- syscall.SIGTERM
	}()

	if err := runServerLifecycle(context.Background(), "server-trace", ":8080", runtime); err != nil {
		t.Fatalf("runServerLifecycle failed: %v", err)
	}

	logged := buf.String()
	for _, needle := range []string{
		"step=listen_start",
		"step=signal_received",
		"step=shutdown_start",
		"step=listen_stopped",
		"step=shutdown_success",
		"trace_id=server-trace",
	} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("expected %q in logs, got %q", needle, logged)
		}
	}
}

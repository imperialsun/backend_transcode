package mistral

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These tests cover timeout defaults and upstream error summarization for the
// Mistral client wrapper.
func TestNewClient_DefaultTimeouts(t *testing.T) {
	client := NewClient("https://api.mistral.ai", "key", 0, 0)

	if client.RequestTimeout != defaultRequestTimeout {
		t.Fatalf("unexpected request timeout: %s", client.RequestTimeout)
	}
	if client.MultipartTimeout != defaultMultipartTimeout {
		t.Fatalf("unexpected multipart timeout: %s", client.MultipartTimeout)
	}
}

// TestSummarizeUpstreamBody_JSONMessage verifies that structured error bodies
// are reduced to a concise summary string.
func TestSummarizeUpstreamBody_JSONMessage(t *testing.T) {
	body := []byte(`{"error":{"message":"rate limit exceeded"}}`)
	summary, preview := summarizeUpstreamBody(body)

	if summary != "rate limit exceeded" {
		t.Fatalf("unexpected summary: %q", summary)
	}
	if preview == "" {
		t.Fatal("expected preview to be populated")
	}
}

// TestSummarizeUpstreamBody_JSONDetailArray verifies that nested detail arrays
// are flattened into a readable message.
func TestSummarizeUpstreamBody_JSONDetailArray(t *testing.T) {
	body := []byte(`{"detail":[{"loc":["body","model"],"msg":"field required"},{"msg":"invalid request"}]}`)
	summary, _ := summarizeUpstreamBody(body)

	if summary != "field required | invalid request" {
		t.Fatalf("unexpected summary: %q", summary)
	}
}

// TestSummarizeUpstreamBody_TextFallback verifies that plain-text bodies are
// preserved as the error summary.
func TestSummarizeUpstreamBody_TextFallback(t *testing.T) {
	body := []byte("upstream gateway timeout")
	summary, preview := summarizeUpstreamBody(body)

	if summary != "upstream gateway timeout" {
		t.Fatalf("unexpected summary: %q", summary)
	}
	if preview != "upstream gateway timeout" {
		t.Fatalf("unexpected preview: %q", preview)
	}
}

// TestClientDoGet_UsesRequestTimeout verifies that the standard request timeout
// is applied to GET requests.
func TestClientDoGet_UsesRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", 20*time.Millisecond, 200*time.Millisecond)
	_, _, err := client.DoGet(context.Background(), "/slow")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestClientDoMultipart_UsesMultipartTimeout verifies that multipart requests
// use the longer audio timeout.
func TestClientDoMultipart_UsesMultipartTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", 20*time.Millisecond, 200*time.Millisecond)
	statusCode, body, err := client.DoMultipart(
		context.Background(),
		"/v1/audio/transcriptions",
		[]byte("payload"),
		"multipart/form-data; boundary=test",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("unexpected status code: %d", statusCode)
	}
	if string(body) != `{"text":"ok"}` {
		t.Fatalf("unexpected body: %q", body)
	}
}

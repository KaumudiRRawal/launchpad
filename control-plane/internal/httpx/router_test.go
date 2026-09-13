package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubPinger struct{ err error }

func (s stubPinger) Ping(context.Context) error { return s.err }

func newTestAPI(pingErr error) *API {
	return &API{
		DB:      stubPinger{err: pingErr},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	}
}

func TestHealthIgnoresDatabase(t *testing.T) {
	// Liveness must stay green even when the database is down, otherwise a
	// database blip triggers a pointless restart loop.
	srv := newTestAPI(errors.New("connection refused")).Routes()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReady(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantStatus int
	}{
		{name: "database reachable", pingErr: nil, wantStatus: http.StatusOK},
		{name: "database down", pingErr: errors.New("connection refused"), wantStatus: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestAPI(tt.pingErr).Routes()

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestUnknownRouteReturnsStructuredError(t *testing.T) {
	srv := newTestAPI(nil).Routes()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	var body ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code != "not_found" {
		t.Errorf("error.code = %q, want not_found", body.Error.Code)
	}
	if body.Error.RequestID == "" {
		t.Error("error.request_id is empty, want the generated request ID")
	}
}

func TestRequestIDFromUpstreamIsPreserved(t *testing.T) {
	srv := newTestAPI(nil).Routes()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set(RequestIDHeader, "upstream-trace-id")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); got != "upstream-trace-id" {
		t.Errorf("%s = %q, want upstream-trace-id", RequestIDHeader, got)
	}
}

func TestPanicBecomesInternalError(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := WithObservability(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// TestObservabilityKeepsTheWriterFlushable guards the Unwrap on the status
// recorder. Every route is mounted behind this middleware, and a wrapper that
// hides the real writer takes flushing away from all of them at once —
// silently, because http.ResponseController reports a writer it cannot reach
// as an error a handler is free to ignore rather than as a panic.
func TestObservabilityKeepsTheWriterFlushable(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	var flushErr error
	handler := WithObservability(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "first\n")
		flushErr = http.NewResponseController(w).Flush()
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stream", nil))

	if flushErr != nil {
		t.Errorf("Flush() through the middleware = %v, want nil", flushErr)
	}
	if !rec.Flushed {
		t.Error("flush did not reach the underlying writer")
	}
}

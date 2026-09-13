package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyLogger
)

// RequestIDHeader is both read from the incoming request and echoed back, so a
// trace ID assigned by an upstream proxy survives into our logs.
const RequestIDHeader = "X-Request-Id"

// RequestIDFrom returns the request ID stored on ctx, or "" if unset.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// LoggerFrom returns the request-scoped logger, falling back to the default
// logger so callers never have to nil-check.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if log, ok := ctx.Value(ctxKeyLogger).(*slog.Logger); ok {
		return log
	}
	return slog.Default()
}

// WithObservability assigns each request an ID, attaches a logger already
// tagged with that ID, logs the outcome, and converts a panic in any handler
// into a 500 instead of killing the whole server.
func WithObservability(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			requestID := r.Header.Get(RequestIDHeader)
			if requestID == "" {
				requestID = newRequestID()
			}
			w.Header().Set(RequestIDHeader, requestID)

			reqLog := log.With(
				slog.String("request_id", requestID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
			)

			ctx := context.WithValue(r.Context(), ctxKeyRequestID, requestID)
			ctx = context.WithValue(ctx, ctxKeyLogger, reqLog)
			r = r.WithContext(ctx)

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			defer func() {
				if p := recover(); p != nil {
					reqLog.Error("panic recovered in handler",
						slog.Any("panic", p),
						slog.String("stack", string(debug.Stack())))

					if !rec.wroteHeader {
						Error(rec, r, http.StatusInternalServerError,
							"internal_error", "an unexpected error occurred")
					}
				}

				reqLog.Info("request completed",
					slog.Int("status", rec.status),
					slog.Duration("duration", time.Since(start)))
			}()

			next.ServeHTTP(rec, r)
		})
	}
}

// statusRecorder captures the status code so it can be logged after the
// handler returns.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.wroteHeader {
		return
	}
	s.status = status
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap keeps the writer this wrapper hides reachable through
// http.ResponseController. Embedding the interface promotes only its three
// methods, so without this every route — they all sit behind this middleware —
// would quietly lose flushing and hijacking: ResponseController reports a
// writer it cannot reach as an error, not a panic.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func newRequestID() string {
	var b [16]byte
	// rand.Read from crypto/rand never returns an error as of Go 1.24.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

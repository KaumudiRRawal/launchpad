// Package httpx contains the control-plane HTTP server: routing, middleware
// and the shared JSON response envelope.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ErrorBody is the single error shape every endpoint returns, so clients have
// exactly one failure format to parse.
type ErrorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id,omitempty"`
	} `json:"error"`
}

// JSON writes v as the response body with the given status code.
func JSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent, so this can only be logged.
		LoggerFrom(r.Context()).Error("encode response body",
			slog.String("error", err.Error()))
	}
}

// Error writes a structured error response, echoing the request ID so a user
// reporting a failure can be traced straight to its log line.
func Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	var body ErrorBody
	body.Error.Code = code
	body.Error.Message = message
	body.Error.RequestID = RequestIDFrom(r.Context())
	JSON(w, r, status, body)
}

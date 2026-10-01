package legacy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

type ErrorKind string

const (
	ErrorKindValidation      ErrorKind = "validation"
	ErrorKindUnauthorized    ErrorKind = "unauthorized"
	ErrorKindForbidden       ErrorKind = "forbidden"
	ErrorKindNotFound        ErrorKind = "not_found"
	ErrorKindConflict        ErrorKind = "conflict"
	ErrorKindInternal        ErrorKind = "internal"
	ErrorKindExternal        ErrorKind = "external"
	ErrorKindRequestTooLarge ErrorKind = "request_too_large"
)

type AppError struct {
	Kind    ErrorKind
	Message string
	Cause   error
}

func (e *AppError) Error() string {
	switch {
	case e.Message != "" && e.Cause != nil:
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	case e.Message != "":
		return e.Message
	case e.Cause != nil:
		return e.Cause.Error()
	default:
		return string(e.Kind)
	}
}

func (e *AppError) Unwrap() error {
	return e.Cause
}

func NewError(kind ErrorKind, message string) error {
	return &AppError{Kind: kind, Message: message}
}

func WrapError(kind ErrorKind, message string, cause error) error {
	return &AppError{Kind: kind, Message: message, Cause: cause}
}

func ErrorKindOf(err error) ErrorKind {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Kind
	}
	return ""
}

func ErrorMessageOf(err error, fallback string) string {
	var appErr *AppError
	if errors.As(err, &appErr) && appErr.Message != "" {
		return appErr.Message
	}
	if fallback != "" {
		return fallback
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func ErrorStatusCodeOf(err error, fallback int) int {
	switch ErrorKindOf(err) {
	case ErrorKindValidation:
		return http.StatusBadRequest
	case ErrorKindUnauthorized:
		return http.StatusUnauthorized
	case ErrorKindForbidden:
		return http.StatusForbidden
	case ErrorKindNotFound:
		return http.StatusNotFound
	case ErrorKindConflict:
		return http.StatusConflict
	case ErrorKindExternal:
		return http.StatusBadGateway
	case ErrorKindRequestTooLarge:
		return http.StatusRequestEntityTooLarge
	case ErrorKindInternal:
		return http.StatusInternalServerError
	default:
		return fallback
	}
}

// logger is the sink for server-side error logging done by the WriteApp*Error helpers below.
// Defaults to slog.Default() so the package works without wiring; SetLogger overrides it once
// at startup — mirrors the ws.Hub logger pattern.
var logger = slog.Default()

// SetLogger overrides the default logger (slog.Default()) used to record errors that reach
// WriteAppMessageError/WriteAppDetailError/WriteAppResultError before they're turned into an
// HTTP response — without this, a 5xx leaves the client with a generic message and the server
// with no trace of what actually failed.
func SetLogger(l *slog.Logger) {
	if l == nil {
		return
	}
	logger = l
}

// logServerError records the real error behind a 5xx response. 4xx responses (validation,
// not found, etc.) are normal client-facing outcomes, not incidents, so they're skipped.
func logServerError(err error, statusCode int) {
	if err == nil || statusCode < 500 {
		return
	}
	logger.Error("app_error", "status_code", statusCode, "kind", string(ErrorKindOf(err)), "error", err)
}

func WriteJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func WriteMessage(w http.ResponseWriter, statusCode int, message string) {
	WriteJSON(w, statusCode, map[string]string{"message": message})
}

func WriteDetail(w http.ResponseWriter, statusCode int, detail string) {
	WriteJSON(w, statusCode, map[string]string{"detail": detail})
}

func WriteResultError(w http.ResponseWriter, statusCode int, message string) {
	WriteJSON(w, statusCode, map[string]any{"result": false, "error": message})
}

func WriteAppMessageError(w http.ResponseWriter, err error, fallbackStatus int, fallbackMessage string) {
	statusCode := ErrorStatusCodeOf(err, fallbackStatus)
	logServerError(err, statusCode)
	WriteMessage(w, statusCode, ErrorMessageOf(err, fallbackMessage))
}

func WriteAppDetailError(w http.ResponseWriter, err error, fallbackStatus int, fallbackMessage string) {
	statusCode := ErrorStatusCodeOf(err, fallbackStatus)
	logServerError(err, statusCode)
	WriteDetail(w, statusCode, ErrorMessageOf(err, fallbackMessage))
}

func WriteAppResultError(w http.ResponseWriter, err error, fallbackStatus int, fallbackMessage string) {
	statusCode := ErrorStatusCodeOf(err, fallbackStatus)
	logServerError(err, statusCode)
	WriteResultError(w, statusCode, ErrorMessageOf(err, fallbackMessage))
}

func DecodeJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return NewError(ErrorKindRequestTooLarge, "Request body is too large")
		}
		return WrapError(ErrorKindValidation, "Invalid request body", err)
	}
	return nil
}

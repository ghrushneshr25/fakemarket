package rest

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ghrushneshr25/fakemarket/internal/application/marketdata"
)

// ErrorBody is the JSON error envelope.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, err error) {
	code, httpStatus := mapError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(ErrorBody{Code: code, Message: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func mapError(err error) (code string, status int) {
	switch {
	case errors.Is(err, marketdata.ErrInvalidQuery),
		errors.Is(err, marketdata.ErrDepthOutOfRange),
		errors.Is(err, marketdata.ErrLimitOutOfRange):
		return "INVALID_ARGUMENT", http.StatusBadRequest
	case errors.Is(err, marketdata.ErrInstrumentUnknown):
		return "NOT_FOUND", http.StatusNotFound
	case errors.Is(err, marketdata.ErrNotReady):
		return "UNAVAILABLE", http.StatusServiceUnavailable
	default:
		return "INTERNAL", http.StatusInternalServerError
	}
}

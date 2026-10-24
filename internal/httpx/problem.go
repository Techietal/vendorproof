// Package httpx holds transport helpers shared by handlers: RFC 9457 problem
// responses, strict JSON decoding, and context helpers.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"vendorproof/internal/logging"
)

// Problem is an RFC 9457 Problem Details document.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

// WriteProblem emits a problem document. detail must be safe for clients.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	writeProblem(w, r, status, detail, nil)
}

// WriteProblemWithErrors is WriteProblem plus field-level validation details.
func WriteProblemWithErrors(w http.ResponseWriter, r *http.Request, status int, detail string, fields map[string]string) {
	writeProblem(w, r, status, detail, fields)
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, detail string, fields map[string]string) {
	p := Problem{
		Type:      "about:blank",
		Title:     http.StatusText(status),
		Status:    status,
		Detail:    detail,
		Instance:  r.URL.Path,
		RequestID: logging.RequestID(r.Context()),
	}
	body := any(p)
	if len(fields) > 0 {
		body = struct {
			Problem
			Errors map[string]string `json:"errors"`
		}{p, fields}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.ErrorContext(r.Context(), "write problem response", "error", err)
	}
}

// ErrBodyTooLarge distinguishes the request-size limit from malformed JSON.
var ErrBodyTooLarge = errors.New("request body too large")

// DecodeJSON strictly decodes exactly one JSON object, enforcing the caller's
// body limit via the server's MaxBytesReader.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			return fmt.Errorf("%w: limit is %d bytes", ErrBodyTooLarge, maxErr.Limit)
		case errors.Is(err, io.EOF):
			return errors.New("request body is empty")
		case errors.Is(err, io.ErrUnexpectedEOF):
			return errors.New("request body is truncated")
		default:
			return errors.New("request body is not valid JSON for this endpoint")
		}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return fmt.Errorf("%w: limit is %d bytes", ErrBodyTooLarge, maxErr.Limit)
		}
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

// WriteDecodeError maps a DecodeJSON failure to 413 or 400.
func WriteDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrBodyTooLarge) {
		WriteProblem(w, r, http.StatusRequestEntityTooLarge, "Request body is too large.")
		return
	}
	WriteProblem(w, r, http.StatusBadRequest, err.Error())
}

// WriteJSON encodes a successful response.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.ErrorContext(r.Context(), "write JSON response", "error", err)
	}
}

// WriteNoContent emits a 204 with no body.
func WriteNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

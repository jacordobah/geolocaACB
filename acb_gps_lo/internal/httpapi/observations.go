package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
)

type Receiver interface {
	Receive(context.Context, string, string, tracking.ObservationInput) (tracking.Acceptance, error)
}

type Observations struct {
	receiver Receiver
	maxBytes int64
	timeout  time.Duration
}

func NewObservations(receiver Receiver, maxBytes int64, timeout time.Duration) (*Observations, error) {
	if receiver == nil || maxBytes < 128 || maxBytes > 65536 || timeout <= 0 {
		return nil, tracking.ErrInvalid
	}
	return &Observations{receiver: receiver, maxBytes: maxBytes, timeout: timeout}, nil
}

type inputJSON struct {
	Sequence   *int64   `json:"sequence"`
	Latitude   *float64 `json:"latitude"`
	Longitude  *float64 `json:"longitude"`
	Accuracy   *float64 `json:"accuracy_m"`
	CapturedAt *string  `json:"captured_at"`
}

func (h *Observations) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	id := r.PathValue("tracking_id")
	if !tracking.ValidID(id) || r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	fields := r.Header.Values("Authorization")
	if len(fields) != 1 || !strings.HasPrefix(fields[0], "Bearer ") || strings.ContainsAny(fields[0][7:], " \t\r\n") || len(fields[0]) > 128 {
		unauthorized(w)
		return
	}
	token := fields[0][7:]
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	if r.ContentLength > h.maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body inputJSON
	if err := decoder.Decode(&body); err != nil {
		decodeError(w, err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			writeError(w, http.StatusBadRequest, "invalid_json")
		} else {
			decodeError(w, err)
		}
		return
	}
	if body.Sequence == nil || body.Latitude == nil || body.Longitude == nil || body.Accuracy == nil || body.CapturedAt == nil {
		writeError(w, http.StatusBadRequest, "invalid_observation")
		return
	}
	captured, err := time.Parse(time.RFC3339Nano, *body.CapturedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_observation")
		return
	}
	input := tracking.ObservationInput{Sequence: *body.Sequence, Latitude: *body.Latitude, Longitude: *body.Longitude, AccuracyMeters: *body.Accuracy, CapturedAt: captured}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	result, err := h.receiver.Receive(ctx, id, token, input)
	if err != nil {
		mapError(w, err)
		return
	}
	status := http.StatusCreated
	acceptance := "accepted"
	if result.Duplicate {
		status = http.StatusOK
		acceptance = "replayed"
	}
	geographic := any("not_evaluated")
	if result.Position.Geographic != nil {
		geographic = result.Position.Geographic
	}
	writeJSON(w, status, map[string]any{"tracking_id": id, "sequence": result.Position.Sequence, "acceptance": acceptance, "received_at": result.Position.ReceivedAt, "expires_at": result.ExpiresAt, "geographic_evaluation": geographic})
}

func decodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json")
}

func mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tracking.ErrUnauthorized), errors.Is(err, tracking.ErrClosed), errors.Is(err, tracking.ErrNotFound):
		unauthorized(w)
	case errors.Is(err, tracking.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_observation")
	case errors.Is(err, tracking.ErrConflict):
		writeError(w, http.StatusConflict, "sequence_conflict")
	case errors.Is(err, tracking.ErrStale):
		writeError(w, http.StatusConflict, "stale_observation")
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable")
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "unauthorized")
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

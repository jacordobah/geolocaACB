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

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/control"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
)

type Controller interface {
	Create(context.Context, control.Principal, string, control.CreateRequest) (control.Created, error)
	Status(context.Context, control.Principal, string) (tracking.Session, error)
	Finish(context.Context, control.Principal, string) (tracking.Session, error)
}

// Control is only registered when a real credential verifier and authorized
// geographic policy source have been supplied by the process composition.
type Control struct {
	service  Controller
	verifier control.Verifier
	timeout  time.Duration
}

func NewControl(service Controller, verifier control.Verifier, timeout time.Duration) (*Control, error) {
	if service == nil || verifier == nil || timeout <= 0 || timeout > 30*time.Second {
		return nil, tracking.ErrInvalid
	}
	return &Control{service: service, verifier: verifier, timeout: timeout}, nil
}

func (h *Control) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	p, err := authenticateControl(ctx, r, h.verifier)
	if err != nil {
		unauthorized(w)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.PathValue("tracking_id") == "":
		h.create(ctx, w, r, p)
	case r.Method == http.MethodGet && r.PathValue("tracking_id") != "":
		h.status(ctx, w, r, p)
	case r.Method == http.MethodPost && r.PathValue("tracking_id") != "":
		h.finish(ctx, w, r, p)
	default:
		writeError(w, 405, "method_not_allowed")
	}
}

func authenticateControl(ctx context.Context, r *http.Request, verifier control.Verifier) (control.Principal, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || len(values[0]) > 4096 || len(values[0]) <= 7 || strings.ContainsAny(values[0][7:], " \t\r\n") {
		return control.Principal{}, control.ErrUnauthenticated
	}
	p, err := verifier.Verify(ctx, values[0][7:])
	now := time.Now()
	if err != nil || !tracking.ValidID(p.Subject) || p.Audience != control.Audience || now.Before(p.NotBefore) || !now.Before(p.ExpiresAt) {
		return control.Principal{}, control.ErrUnauthenticated
	}
	return p, nil
}

func (h *Control) create(ctx context.Context, w http.ResponseWriter, r *http.Request, p control.Principal) {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !tracking.ValidID(keys[0]) {
		writeError(w, 400, "invalid_idempotency_key")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 415, "unsupported_media_type")
		return
	}
	const maxBytes int64 = 4096
	if r.ContentLength > maxBytes {
		writeError(w, 413, "request_too_large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request control.CreateRequest
	if err = decoder.Decode(&request); err != nil {
		decodeError(w, err)
		return
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		writeError(w, 400, "invalid_json")
		return
	}
	result, err := h.service.Create(ctx, p, keys[0], request)
	if err != nil {
		controlError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"tracking_id": result.Session.ID, "session": result.Session, "observation_credential": result.Credential, "credential_status": result.CredentialStatus})
}

func (h *Control) status(ctx context.Context, w http.ResponseWriter, r *http.Request, p control.Principal) {
	id := r.PathValue("tracking_id")
	session, err := h.service.Status(ctx, p, id)
	if err != nil {
		controlError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"session": session})
}

func (h *Control) finish(ctx context.Context, w http.ResponseWriter, r *http.Request, p control.Principal) {
	id := r.PathValue("tracking_id")
	if r.ContentLength > 0 {
		writeError(w, 400, "unexpected_body")
		return
	}
	session, err := h.service.Finish(ctx, p, id)
	if err != nil {
		controlError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"session": session})
}

func controlError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, control.ErrUnauthenticated):
		unauthorized(w)
	case errors.Is(err, control.ErrForbidden):
		writeError(w, 403, "forbidden")
	case errors.Is(err, tracking.ErrNotFound), errors.Is(err, routes.ErrNotFound):
		writeError(w, 404, "not_found")
	case errors.Is(err, tracking.ErrConflict):
		writeError(w, 409, "idempotency_conflict")
	case errors.Is(err, tracking.ErrRouteIneligible), errors.Is(err, tracking.ErrInvalidGeometry):
		writeError(w, 422, "route_ineligible")
	case errors.Is(err, tracking.ErrInvalid):
		writeError(w, 400, "invalid_request")
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, 503, "temporarily_unavailable")
	}
}

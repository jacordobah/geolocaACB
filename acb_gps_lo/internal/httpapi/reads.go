package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/control"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
)

type ReadCases interface {
	Latest(context.Context, control.Principal, string) (tracking.Session, tracking.Position, error)
	History(context.Context, control.Principal, string, int64, int64) (tracking.Session, tracking.Page, error)
}

type Reads struct {
	service  ReadCases
	verifier control.Verifier
	timeout  time.Duration
	maxPage  int64
	maxBytes int
}

func NewReads(service ReadCases, verifier control.Verifier, timeout time.Duration, maxPage int64, maxBytes int) (*Reads, error) {
	if service == nil || verifier == nil || timeout <= 0 || timeout > 30*time.Second || maxPage < 1 || maxPage > 100 || maxBytes < 1024 || maxBytes > 4<<20 {
		return nil, tracking.ErrInvalid
	}
	return &Reads{service: service, verifier: verifier, timeout: timeout, maxPage: maxPage, maxBytes: maxBytes}, nil
}

func (h *Reads) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		writeError(w, 405, "method_not_allowed")
		return
	}
	id := r.PathValue("tracking_id")
	if !tracking.ValidID(id) {
		writeError(w, 400, "invalid_tracking_id")
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
	case strings.HasSuffix(r.URL.Path, "/latest-position"):
		if r.URL.RawQuery != "" {
			writeError(w, 400, "invalid_query")
			return
		}
		h.latest(ctx, w, p, id)
	case strings.HasSuffix(r.URL.Path, "/positions"):
		after, limit, err := h.pagination(r.URL.RawQuery)
		if err != nil {
			writeError(w, 400, "invalid_pagination")
			return
		}
		h.history(ctx, w, p, id, after, limit)
	default:
		writeError(w, 404, "not_found")
	}
}

func (h *Reads) pagination(raw string) (int64, int64, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return 0, 0, tracking.ErrInvalid
	}
	for key, values := range query {
		if (key != "after_sequence" && key != "limit") || len(values) != 1 {
			return 0, 0, tracking.ErrInvalid
		}
	}
	limit := h.maxPage
	if limit > 50 {
		limit = 50
	}
	after := int64(0)
	if values, ok := query["after_sequence"]; ok {
		if !decimal(values[0]) {
			return 0, 0, tracking.ErrInvalid
		}
		after, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || after < 0 || after > (1<<53)-1 {
			return 0, 0, tracking.ErrInvalid
		}
	}
	if values, ok := query["limit"]; ok {
		if !decimal(values[0]) {
			return 0, 0, tracking.ErrInvalid
		}
		limit, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || limit < 1 || limit > h.maxPage {
			return 0, 0, tracking.ErrInvalid
		}
	}
	return after, limit, nil
}

func decimal(raw string) bool {
	if raw == "" {
		return false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

type positionView struct {
	Sequence             int64     `json:"sequence"`
	Latitude             float64   `json:"latitude"`
	Longitude            float64   `json:"longitude"`
	AccuracyMeters       *float64  `json:"accuracy_m"`
	CapturedAt           time.Time `json:"captured_at"`
	ReceivedAt           time.Time `json:"received_at"`
	GeographicEvaluation any       `json:"geographic_evaluation"`
}

func viewPosition(p tracking.Position) positionView {
	evaluation := any("not_evaluated")
	if p.Geographic != nil {
		evaluation = p.Geographic
	}
	return positionView{Sequence: p.Sequence, Latitude: p.Latitude, Longitude: p.Longitude, AccuracyMeters: p.AccuracyMeters, CapturedAt: p.CapturedAt, ReceivedAt: p.ReceivedAt, GeographicEvaluation: evaluation}
}

func (h *Reads) latest(ctx context.Context, w http.ResponseWriter, p control.Principal, id string) {
	session, position, err := h.service.Latest(ctx, p, id)
	if errors.Is(err, control.ErrNoObservations) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if errors.Is(err, control.ErrPositionExpired) {
		writeError(w, http.StatusGone, "position_expired")
		return
	}
	if err != nil {
		controlError(w, err)
		return
	}
	age := time.Since(position.ReceivedAt)
	if age < 0 {
		age = 0
	}
	h.writeLimited(w, 200, map[string]any{"tracking_id": id, "tracking_state": session.State, "position": viewPosition(position), "age_ms": age.Milliseconds()})
}

func (h *Reads) history(ctx context.Context, w http.ResponseWriter, p control.Principal, id string, after, limit int64) {
	session, page, err := h.service.History(ctx, p, id, after, limit)
	if err != nil {
		controlError(w, err)
		return
	}
	positions := make([]positionView, 0, len(page.Positions))
	for _, position := range page.Positions {
		positions = append(positions, viewPosition(position))
	}
	h.writeLimited(w, 200, map[string]any{"tracking_id": id, "tracking_state": session.State, "positions": positions, "next_after_sequence": page.NextSequence, "has_more": page.HasMore})
}

func (h *Reads) writeLimited(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		writeError(w, 503, "response_unavailable")
		return
	}
	if len(body) > h.maxBytes {
		writeError(w, 422, "page_too_large")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

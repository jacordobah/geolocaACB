package operationshttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
)

// DeliveryError classifies failures without retaining response bodies or secrets.
type DeliveryError struct {
	Permanent  bool
	Reason     string
	RetryAfter time.Duration
}

func (e *DeliveryError) Error() string { return e.Reason }

type Client struct {
	endpoint    string
	token       string
	http        *http.Client
	maxResponse int64
}

func New(endpoint, token string, timeout time.Duration, maxResponse int64) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Path == "" || strings.HasSuffix(u.Path, "/") || strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") || timeout <= 0 || maxResponse < 32 || maxResponse > 1<<20 {
		return nil, errors.New("invalid Operations HTTP configuration")
	}
	return &Client{endpoint: u.String(), token: token, http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, maxResponse: maxResponse}, nil
}

type envelope struct {
	SchemaVersion string         `json:"schema_version"`
	Event         tracking.Event `json:"event"`
}
type acknowledgement struct {
	SchemaVersion string `json:"schema_version"`
	EventID       string `json:"event_id"`
	Status        string `json:"status"`
}

// Deliver requires a durable, event-specific acknowledgement. The provisional
// endpoint and schema must be agreed with Operations before enabling dispatch.
func (c *Client) Deliver(ctx context.Context, event tracking.Event) error {
	if err := event.Validate(); err != nil {
		return &DeliveryError{Permanent: true, Reason: "invalid_event"}
	}
	body, err := json.Marshal(envelope{SchemaVersion: "gps.event.v1", Event: event})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Idempotency-Key", event.ID)
	res, err := c.http.Do(req)
	if err != nil {
		return &DeliveryError{Reason: "transport_failure"}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusRequestTimeout || res.StatusCode >= 500 {
		var wait time.Duration
		if n, e := time.ParseDuration(res.Header.Get("Retry-After") + "s"); e == nil && n > 0 && n <= time.Hour {
			wait = n
		} else if when, e := http.ParseTime(res.Header.Get("Retry-After")); e == nil {
			wait = time.Until(when)
			if wait < 0 || wait > time.Hour {
				wait = 0
			}
		}
		return &DeliveryError{Reason: fmt.Sprintf("receiver_http_%d", res.StatusCode), RetryAfter: wait}
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return &DeliveryError{Permanent: true, Reason: fmt.Sprintf("receiver_http_%d", res.StatusCode)}
	}
	media, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return &DeliveryError{Permanent: true, Reason: "invalid_ack_content_type"}
	}
	limited := io.LimitReader(res.Body, c.maxResponse+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return &DeliveryError{Reason: "ack_read_failure"}
	}
	if int64(len(data)) > c.maxResponse {
		return &DeliveryError{Permanent: true, Reason: "ack_too_large"}
	}
	var ack acknowledgement
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ack); err != nil {
		return &DeliveryError{Permanent: true, Reason: "invalid_ack"}
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || ack.SchemaVersion != "gps.ack.v1" || ack.EventID != event.ID || (ack.Status != "persisted" && ack.Status != "already_persisted") {
		return &DeliveryError{Permanent: true, Reason: "ack_mismatch"}
	}
	return nil
}

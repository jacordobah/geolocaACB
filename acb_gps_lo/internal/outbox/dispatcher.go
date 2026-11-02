package outbox

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/operationshttp"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/redisstore"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
)

type Config struct {
	Workers            int
	Poll               time.Duration
	Lease              time.Duration
	RetryMin           time.Duration
	RetryMax           time.Duration
	MaxPerHour         int
	DeliveredRetention time.Duration
}

type Dispatcher struct {
	store  *redisstore.Store
	client *operationshttp.Client
	cfg    Config
	logger *slog.Logger
}

func New(store *redisstore.Store, client *operationshttp.Client, cfg Config, logger *slog.Logger) (*Dispatcher, error) {
	if store == nil || client == nil || logger == nil || cfg.Workers < 1 || cfg.Workers > 32 || cfg.Poll < 100*time.Millisecond || cfg.Lease < time.Second || cfg.RetryMin < time.Second || cfg.RetryMax < cfg.RetryMin || cfg.MaxPerHour < 1 || cfg.DeliveredRetention < time.Hour {
		return nil, tracking.ErrInvalid
	}
	return &Dispatcher{store: store, client: client, cfg: cfg, logger: logger}, nil
}

// Start stops acquiring on cancellation and drains in-flight requests. The
// caller waits on done within its shutdown deadline; leases survive a crash.
func (d *Dispatcher) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(d.cfg.Workers + 1)
	go func() { defer wg.Done(); d.monitor(ctx) }()
	for i := 0; i < d.cfg.Workers; i++ {
		go func() { defer wg.Done(); d.deliverLoop(ctx) }()
	}
	go func() { wg.Wait(); close(done) }()
	return done
}

func (d *Dispatcher) monitor(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		pending, blocked, oldestDue, err := d.store.OutboxStats(ctx)
		if err != nil {
			d.logger.Warn("outbox statistics unavailable", "error", err)
		} else {
			age := time.Duration(0)
			if !oldestDue.IsZero() && oldestDue.Before(time.Now()) {
				age = time.Since(oldestDue)
			}
			d.logger.Info("outbox status", "pending", pending, "blocked", blocked, "oldest_due_age", age.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// StartRecovery is independent of HTTP delivery and runs even when Operations
// has no agreed endpoint. Each tick performs bounded Redis work.
func StartRecovery(ctx context.Context, store *redisstore.Store, logger *slog.Logger, poll time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() { defer close(done); recoverLoop(ctx, store, logger, poll) }()
	return done
}

func recoverLoop(ctx context.Context, store *redisstore.Store, logger *slog.Logger, poll time.Duration) {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var cursor uint64
	var cycles int
	for {
		if ctx.Err() != nil {
			return
		}
		var err error
		cursor, err = store.DiscoverPending(ctx, cursor, 50)
		if err != nil {
			logger.Error("pending discovery failed", "error", err)
		}
		ids, err := store.PendingSessions(ctx, 20)
		if err != nil {
			logger.Error("pending recovery query failed", "error", err)
		} else {
			for _, id := range ids {
				if ctx.Err() != nil {
					return
				}
				if err := store.RecoverPending(ctx, id); err != nil {
					count, deferErr := store.DeferRecovery(ctx, id, time.Second, 5*time.Minute)
					logger.Error("pending transition recovery failed", "tracking_id", id, "attempts", count, "error", err, "defer_error", deferErr)
				} else {
					logger.Info("pending transition recovered", "tracking_id", id)
				}
			}
		}
		if _, err := store.CleanPendingEvents(ctx, 20); err != nil {
			logger.Error("pending event guard cleanup failed", "error", err)
		}
		cycles++
		if cycles%30 == 0 {
			pending, blocked, oldest, err := store.OutboxStats(ctx)
			if err == nil {
				age := time.Duration(0)
				if !oldest.IsZero() && oldest.Before(time.Now()) {
					age = time.Since(oldest)
				}
				logger.Info("outbox retained status", "pending", pending, "blocked", blocked, "oldest_due_age", age.String())
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) deliverLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.Poll)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		claimed, ok, err := d.store.ClaimEvent(ctx, d.cfg.Lease, d.cfg.MaxPerHour, d.cfg.DeliveredRetention)
		if err != nil && !errors.Is(err, tracking.ErrPending) {
			d.logger.Error("event claim failed", "error", err)
		}
		if ok {
			d.deliver(claimed)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) deliver(claim redisstore.ClaimedEvent) {
	// The HTTP client's timeout is shorter than the claim lease. Canceling the
	// acquisition loop does not abort an in-flight delivery on SIGTERM.
	err := d.client.Deliver(context.Background(), claim.Event)
	if err == nil {
		settleErr := d.store.SettleEvent(context.Background(), claim.Event.ID, claim.Token, "delivered", d.cfg.DeliveredRetention.Milliseconds(), "")
		if settleErr != nil {
			state, checkErr := d.store.DeliveryState(context.Background(), claim.Event.ID)
			if checkErr != nil || state != "delivered" {
				d.logger.Error("delivery acknowledgement storage uncertain", "event_id", claim.Event.ID, "error", settleErr, "check_error", checkErr)
				return
			}
		}
		d.logger.Info("event delivered", "event_id", claim.Event.ID, "attempt", claim.Attempts)
		return
	}
	var deliveryErr *operationshttp.DeliveryError
	state, reason := "pending", "transport_failure"
	var retryAfter time.Duration
	if errors.As(err, &deliveryErr) {
		reason = deliveryErr.Reason
		retryAfter = deliveryErr.RetryAfter
		if deliveryErr.Permanent {
			state = "blocked"
		}
	}
	var value int64
	if state == "pending" {
		delay := d.backoff(claim.Attempts)
		if retryAfter > delay {
			delay = retryAfter
		}
		value = delay.Milliseconds()
	}
	if settleErr := d.store.SettleEvent(context.Background(), claim.Event.ID, claim.Token, state, value, reason); settleErr != nil {
		d.logger.Error("event settlement uncertain", "event_id", claim.Event.ID, "error", settleErr)
		return
	}
	if state == "blocked" {
		d.logger.Error("event blocked", "event_id", claim.Event.ID, "reason", reason)
	} else {
		d.logger.Warn("event scheduled for retry", "event_id", claim.Event.ID, "reason", reason, "attempt", claim.Attempts)
	}
}

func (d *Dispatcher) backoff(attempt int) time.Duration {
	delay := d.cfg.RetryMin
	for i := 1; i < attempt && delay < d.cfg.RetryMax; i++ {
		if delay > d.cfg.RetryMax/2 {
			return d.cfg.RetryMax
		}
		delay *= 2
	}
	if delay > d.cfg.RetryMax {
		delay = d.cfg.RetryMax
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		delay += time.Duration(binary.LittleEndian.Uint64(b[:]) % uint64(delay/4+1))
	}
	return minDuration(delay, d.cfg.RetryMax)
}
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

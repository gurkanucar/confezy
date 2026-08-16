// Package webhook delivers change notifications. When anything inside an
// environment changes, every enabled webhook on that environment receives a
// request with an empty body — a signal to re-fetch the snapshot, not a
// description of what changed. Nothing about the change is shipped to the
// receiver, so pointing a webhook somewhere cannot leak config values.
package webhook

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"confezy/internal/db"
	"confezy/internal/model"
)

const (
	// deliveryTimeout caps a single attempt. A slow receiver must not pin a
	// worker forever.
	deliveryTimeout = 10 * time.Second

	// coalesceWindow batches the changes that arrive close together. Toggling
	// five flags in a row is one thing the receiver needs to know about, not
	// five, and the request carries no detail that would distinguish them.
	coalesceWindow = 750 * time.Millisecond

	// queueSize bounds the pending-notification buffer. Overflow is dropped and
	// logged rather than allowed to block a request handler.
	queueSize = 256
)

// ErrInvalidURL is returned when a webhook target is not a usable http(s) URL.
var ErrInvalidURL = errors.New("webhook URL must be an absolute http:// or https:// URL")

// ValidateURL checks a target before it is stored. Only http and https are
// accepted: other schemes (file:, gopher:) are not something this should reach.
func ValidateURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ErrInvalidURL
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ErrInvalidURL
	}
	if parsed.Host == "" {
		return ErrInvalidURL
	}
	return nil
}

// Dispatcher owns the delivery goroutine.
type Dispatcher struct {
	db     *db.DB
	client *http.Client
	queue  chan int64
}

// New builds a dispatcher. Call Run to start delivering.
func New(database *db.DB) *Dispatcher {
	return &Dispatcher{
		db: database,
		client: &http.Client{
			Timeout: deliveryTimeout,
			// Deliveries are fire-and-forget signals; chasing redirects around
			// would only make failures harder to read.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		queue: make(chan int64, queueSize),
	}
}

// Notify records that an environment changed. Safe to call from a request
// handler: it never blocks, and drops rather than waits if the queue is full.
func (d *Dispatcher) Notify(envID int64) {
	select {
	case d.queue <- envID:
	default:
		log.Printf("webhook: queue full, dropped notification for environment %d", envID)
	}
}

// Run delivers notifications until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	pending := make(map[int64]bool)
	ticker := time.NewTicker(coalesceWindow)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case envID := <-d.queue:
			pending[envID] = true

		case <-ticker.C:
			if len(pending) == 0 {
				continue
			}
			envIDs := make([]int64, 0, len(pending))
			for id := range pending {
				envIDs = append(envIDs, id)
			}
			clear(pending)

			for _, envID := range envIDs {
				d.deliverEnvironment(ctx, envID)
			}
		}
	}
}

// deliverEnvironment fires every enabled webhook on one environment.
func (d *Dispatcher) deliverEnvironment(ctx context.Context, envID int64) {
	hooks, err := d.db.ListEnabledWebhooks(ctx, envID)
	if err != nil {
		log.Printf("webhook: list for environment %d: %v", envID, err)
		return
	}
	for _, hook := range hooks {
		if err := d.Deliver(ctx, hook); err != nil {
			log.Printf("webhook %d (%s): %v", hook.ID, hook.URL, err)
		}
	}
}

// Deliver sends one webhook and records the outcome. The returned error
// describes a failed delivery; the result is stored either way, so the panel
// can show whether the receiver is reachable. Exported so the panel's "Test"
// button can reuse exactly the same path as an automatic delivery.
func (d *Dispatcher) Deliver(ctx context.Context, hook model.Webhook) error {
	reqCtx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()

	method := hook.Method
	if method == "" {
		method = "PATCH"
	}

	// No body: the receiver is being told to re-fetch, not handed the change.
	req, err := http.NewRequestWithContext(reqCtx, method, hook.URL, nil)
	if err != nil {
		d.record(hook.ID, 0, err.Error())
		return err
	}

	req.Header.Set("User-Agent", "confezy-webhook/1")
	req.Header.Set("Content-Length", "0")
	for name, value := range hook.Headers {
		// Host cannot be set through the header map; net/http reads it from
		// the request field.
		if strings.EqualFold(name, "Host") {
			req.Host = value
			continue
		}
		req.Header.Set(name, value)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		d.record(hook.ID, 0, err.Error())
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := fmt.Sprintf("receiver answered %s", resp.Status)
		d.record(hook.ID, resp.StatusCode, msg)
		return errors.New(msg)
	}

	d.record(hook.ID, resp.StatusCode, "")
	return nil
}

// record stores the attempt result on its own context: the delivery context may
// already be cancelled, and the outcome is still worth writing down.
func (d *Dispatcher) record(id int64, status int, deliveryErr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.db.RecordWebhookResult(ctx, id, status, deliveryErr); err != nil {
		log.Printf("webhook: record result for %d: %v", id, err)
	}
}

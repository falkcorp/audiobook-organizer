// file: core/integrations/notify/deliver.go
// version: 1.0.0
// guid: 6b1e94d8-2f07-4c35-8a73-d5c0e91b2f48
// last-edited: 2026-11-13

package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
)

// The delivery topic and its consumer. The topic is core's (its first
// segment is "core", so only Writer("core") stages it), declared durable with
// the manage permission: the entry carries the notification's text, which is
// for whoever manages integrations, not for every subscriber.
const (
	DeliveryTopicName = "core.notify.delivery.requested.v1"
	DeliverConsumer   = "core.notify.deliver"
)

// Delivery is the payload of one outbox entry: the notification and the one
// channel it goes to. Publish stages one per resolved target.
type Delivery struct {
	Notification Notification `json:"notification"`
	Target       Target       `json:"target"`
}

// DeliveryTopic is the outbox topic Publish stages into.
func DeliveryTopic() outbox.Topic[Delivery] { return outbox.NewTopic[Delivery](DeliveryTopicName) }

// DeliveryTopicSpec is the catalog entry of DeliveryTopic, for the kernel's
// Registrar.Topics. 16 partitions, ordered by channel instance.
func DeliveryTopicSpec() events.TopicSpec {
	return DeliveryTopic().Spec(events.TopicSpec{Permission: rbac.Need(integrations.ManagePerm)}, 16)
}

// dispatchCeiling is the outbox's own MaxAttempts for the consumer. The
// handler enforces core.notify.max_attempts and max_age itself, because they
// are hot-reloadable and an outbox option is fixed at registration; the
// ceiling only stops a handler that never decides.
const dispatchCeiling = 1 << 30

// retryFreeDelay is how long an entry waits when its channel could not be
// tried because the integrations switch or the instance is off: the entry
// stays pending, and no attempt is used, until someone turns it back on.
const retryFreeDelay = time.Minute

// Consumer returns the handler of DeliveryTopic: register it with
// Outbox.Register("core", notify.Consumer(svc)) (and list DeliverConsumer in
// outbox.Options.CoreConsumers). It is unordered: a channel that is down must
// not stop another channel's entry, only retry on its own schedule.
func (s *Service) Consumer() outbox.Handler {
	return outbox.Consume(DeliveryTopic(), DeliverConsumer, s.deliver,
		outbox.Unordered(), outbox.MaxAttempts(dispatchCeiling))
}

// deliver sends one delivery. The ctx is the relay's (bounded by
// core.outbox.handler_timeout), never the publisher's, which ended long ago.
func (s *Service) deliver(ctx context.Context, m outbox.Message[Delivery]) error {
	cfg := s.config()
	d := m.Payload
	ref := d.Target.Instance

	if age := s.now().Sub(m.EnqueuedAt); age > cfg.MaxAge {
		s.inst.delivery(ref, "expired", "", -1)
		return fmt.Errorf("%w: %s to %s is %s old, past core.notify.max_age %s",
			outbox.ErrPermanent, d.Notification.Topic, ref, age.Round(time.Second), cfg.MaxAge)
	}

	nr, release, err := integrations.Lease[Notifier](ctx, s.o.Registry, ref)
	if err != nil {
		if errors.Is(err, integrations.ErrDisabled) {
			// The switch (or this instance) is off. Nothing was tried, so keep
			// the entry pending and keep its attempts for when it is on.
			s.inst.delivery(ref, "disabled", "", -1)
			return outbox.RetryFree(retryFreeDelay, err)
		}
		s.inst.delivery(ref, classLabel(ctx, err), "", -1)
		return s.retryOrDeadLetter(m, cfg, err, 0)
	}
	defer release()
	adapter := ""
	if ic, ok := s.o.Registry.Config(ref); ok {
		adapter = string(ic.Adapter)
	}

	sctx, cancel := context.WithTimeout(ctx, cfg.SendTimeout)
	defer cancel()
	start := s.now()
	err = nr.Send(sctx, d.Notification, d.Target)
	took := s.now().Sub(start)
	if err == nil {
		s.inst.delivery(ref, "ok", adapter, took)
		return nil
	}
	if errors.Is(err, integrations.ErrDisabled) {
		// The switch went off between the lease and the send: the same case
		// as a refused lease. Nothing reached the channel.
		s.inst.delivery(ref, "disabled", "", -1)
		return outbox.RetryFree(retryFreeDelay, err)
	}
	class := integrations.ClassOf(ctx, err)
	s.inst.delivery(ref, class.String(), adapter, took)
	s.log.Warn("notify: delivery failed", "instance", ref.String(), "topic", d.Notification.Topic,
		"notification", d.Notification.ID, "class", class.String(), "attempt", m.Attempt)

	switch class {
	case integrations.ClassCanceled:
		// The relay is stopping: not a verdict on the channel. Hand the error
		// back untouched; the relay leaves the entry pending on shutdown.
		return err
	case integrations.ClassTransient, integrations.ClassRateLimited, integrations.ClassQuota:
		return s.retryOrDeadLetter(m, cfg, err, retryAfterOf(err))
	case integrations.ClassRefusedLocal:
		if until, ok := refusalWait(s.now(), err); ok {
			return s.retryOrDeadLetter(m, cfg, err, until)
		}
	}
	// Auth, Invalid, NotFound, Unsupported, BadOutput, Unknown, and a local
	// refusal that waiting cannot lift (the egress guard): a human must act.
	return fmt.Errorf("%w: %w", outbox.ErrPermanent, err)
}

// classLabel is the class label of an error the lease returned.
func classLabel(ctx context.Context, err error) string {
	return integrations.ClassOf(ctx, err).String()
}

// retryAfterOf is the Retry-After an adapter's error carries, 0 if none.
func retryAfterOf(err error) time.Duration {
	if e, ok := errors.AsType[*integrations.Error](err); ok && e.RetryAfter > 0 {
		return e.RetryAfter
	}
	return 0
}

// refusalWait reports whether a local refusal is one that time lifts (a hold,
// an open breaker, a spent daily budget) and how long to wait. An egress
// refusal is configuration: it never lifts by itself.
func refusalWait(now time.Time, err error) (time.Duration, bool) {
	why, ok := providerhttp.ReasonOf(err)
	if !ok {
		return 0, false
	}
	switch why {
	case providerhttp.ReasonHold, providerhttp.ReasonBreaker, providerhttp.ReasonQuota:
		if r, ok := errors.AsType[*providerhttp.Refusal](err); ok && r.Until.After(now) {
			return r.Until.Sub(now), true
		}
		return 0, true
	}
	return 0, false
}

// retryOrDeadLetter returns err for another attempt, after wait when the
// remote or the gate said how long, unless this was the last attempt
// core.notify.max_attempts allows, which dead-letters it.
func (s *Service) retryOrDeadLetter(m outbox.Message[Delivery], cfg Config, err error, wait time.Duration) error {
	if m.Attempt >= cfg.MaxAttempts {
		return fmt.Errorf("%w: gave up after %d attempts: %w", outbox.ErrPermanent, m.Attempt, err)
	}
	if wait > 0 {
		return outbox.RetryAfter(wait, err)
	}
	return err
}

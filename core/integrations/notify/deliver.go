// file: core/integrations/notify/deliver.go
// version: 1.1.1
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
// Registrar.Topics. 16 partitions, keyed by channel instance: the key keeps one
// channel's entries together, but a hash into 16 lets two channels share a
// partition, and the consumer is unordered.
func DeliveryTopicSpec() events.TopicSpec {
	return DeliveryTopic().Spec(events.TopicSpec{Permission: rbac.Need(integrations.ManagePerm)}, 16)
}

// dispatchCeiling is the outbox's own MaxAttempts for the consumer. The
// handler enforces core.notify.max_attempts and max_age itself, because they
// are hot-reloadable and an outbox option is fixed at registration; the
// ceiling only stops a handler that never decides.
const dispatchCeiling = 1 << 30

// retryFreeDelay is how long an entry waits when its channel could not be
// tried because the global core.integrations.disabled switch is on: the entry
// stays pending, and no attempt is used, until someone turns it back on. (An
// instance that is switched off is different: see disabledRetry.)
const retryFreeDelay = time.Minute

// defaultRetryAfterMax caps a receiver's Retry-After when neither the instance
// nor its adapter sets integrations.Limits.RetryAfterMax; it is providerhttp's
// own default.
const defaultRetryAfterMax = 5 * time.Minute

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
		if relayStopping(ctx) {
			// The relay is stopping: not a verdict on the channel, and no
			// attempt. The relay leaves the entry pending on shutdown.
			return err
		}
		if errors.Is(err, integrations.ErrDisabled) {
			return s.disabledRetry(m, cfg, ref, err)
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
	if relayStopping(ctx) {
		// The relay is stopping, so the send was cut short by us and says
		// nothing about the channel: hand the error back untouched and use no
		// attempt. This is decided on the relay's ctx, never on the send's: an
		// adapter classifies against the send ctx, where its own 15s deadline
		// looks like a cancellation.
		s.inst.delivery(ref, integrations.ClassCanceled.String(), adapter, took)
		return err
	}
	if errors.Is(err, integrations.ErrDisabled) {
		// The switch (or the instance) went off between the lease and the
		// send: the same case as a refused lease. Nothing reached the channel.
		return s.disabledRetry(m, cfg, ref, err)
	}
	class := integrations.ClassOf(ctx, err)
	if class == integrations.ClassCanceled || (class == integrations.ClassUnknown && sctx.Err() != nil) {
		// The relay is not stopping, so a cancellation or deadline the adapter
		// saw was the send's own timeout (a receiver that hangs), or the
		// handler's own deadline: a transient failure, counted, and
		// dead-lettered at max_attempts. An adapter that lost the cause (an
		// error that says only "use of closed network connection") reads the
		// same once the send's ctx is done.
		class = integrations.ClassTransient
	}
	s.inst.delivery(ref, class.String(), adapter, took)
	s.log.Warn("notify: delivery failed", "instance", ref.String(), "topic", d.Notification.Topic,
		"notification", d.Notification.ID, "class", class.String(), "attempt", m.Attempt)

	switch class {
	case integrations.ClassTransient, integrations.ClassRateLimited, integrations.ClassQuota:
		return s.retryOrDeadLetter(m, cfg, err, s.clampRetryAfter(ref, retryAfterOf(err)))
	case integrations.ClassRefusedLocal:
		if until, ok := refusalWait(s.now(), err); ok {
			return s.retryOrDeadLetter(m, cfg, err, until)
		}
	}
	// Auth, Invalid, NotFound, Unsupported, BadOutput, Unknown, and a local
	// refusal that waiting cannot lift (the egress guard): a human must act.
	return fmt.Errorf("%w: %w", outbox.ErrPermanent, err)
}

// disabledRetry is the handler's answer to a refusal because integrations are
// switched off. The global core.integrations.disabled switch is an operator
// pause: nothing was tried, so the entry stays pending and keeps its attempts
// (outbox.RetryFree). An instance that was switched off after the entry was
// staged is a counted retry: Publish no longer stages deliveries for a
// disabled instance, so this one is a channel the operator turned off with
// work in flight, and its entries must not sit for max_age unaccounted, taking
// slots of a partition other channels share. They retry on the outbox's
// backoff and dead-letter at max_attempts, where an operator can retry them
// once the channel is back.
func (s *Service) disabledRetry(m outbox.Message[Delivery], cfg Config, ref integrations.Ref, err error) error {
	s.inst.delivery(ref, "disabled", "", -1)
	if s.instanceDisabled(ref) {
		return s.retryOrDeadLetter(m, cfg, err, 0)
	}
	return outbox.RetryFree(retryFreeDelay, err)
}

// instanceDisabled reports whether the registry holds ref switched off.
func (s *Service) instanceDisabled(ref integrations.Ref) bool {
	ic, ok := s.o.Registry.Config(ref)
	return ok && !ic.Enabled
}

// retryAfterMax is the longest a receiver's Retry-After is honoured for ref:
// the instance's Limits.RetryAfterMax, else its adapter's, else 5 minutes.
func (s *Service) retryAfterMax(ref integrations.Ref) time.Duration {
	ic, ok := s.o.Registry.Config(ref)
	if !ok {
		return defaultRetryAfterMax
	}
	if ic.Limits != nil && ic.Limits.RetryAfterMax > 0 {
		return ic.Limits.RetryAfterMax
	}
	for meta := range s.o.Registry.Adapters(ref.Kind) {
		if meta.ID == ic.Adapter && meta.Limits.RetryAfterMax > 0 {
			return meta.Limits.RetryAfterMax
		}
	}
	return defaultRetryAfterMax
}

// clampRetryAfter bounds a Retry-After an adapter reported. An adapter's
// answer is the remote's word, and one year is a legal value of the header:
// the clamp is here, once, so every adapter has it.
func (s *Service) clampRetryAfter(ref integrations.Ref, wait time.Duration) time.Duration {
	return min(wait, s.retryAfterMax(ref))
}

// classLabel is the class label of an error the lease returned.
func classLabel(ctx context.Context, err error) string {
	return integrations.ClassOf(ctx, err).String()
}

// relayStopping reports whether the relay is shutting down: its ctx was
// canceled. A ctx that ended by its deadline is not that: it is the handler's
// own time limit (core.outbox.handler_timeout), which a send_timeout set at or
// above it can reach, and a handler that ran out of time has failed an attempt.
func relayStopping(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled)
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
		// Never park an entry past the moment it expires: the next attempt
		// then finds it past max_age and dead-letters it, instead of the
		// entry holding one of its partition's retry slots for the length of
		// a hold or a Retry-After.
		remaining := max(cfg.MaxAge-s.now().Sub(m.EnqueuedAt), time.Second)
		return outbox.RetryAfter(min(wait, remaining), err)
	}
	return err
}

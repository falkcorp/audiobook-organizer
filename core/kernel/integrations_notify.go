// file: core/kernel/integrations_notify.go
// version: 0.1.0
// guid: aff89e7a-0eb8-4c08-93b8-155f6376e0c1
// last-edited: 2026-11-22

package kernel

import (
	"context"

	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/outbox"
)

// notifyConfig is core.notify as notify.Config: send_timeout, max_attempts and
// max_age. The service enforces max_attempts and max_age itself (they are
// hot-reloadable and an outbox option is fixed at registration).
func notifyConfig(v notifySettings) notify.Config {
	return notify.Config{SendTimeout: v.SendTimeout.Std(), MaxAttempts: v.MaxAttempts, MaxAge: v.MaxAge.Std()}
}

// buildNotify builds the notify Service over c/notify/, the outbox and the
// Registry. Its events catalog is read lazily, so the service may be built
// before P7 declares the topics. It has nothing to close.
func (s *integrationsServices) buildNotify(ctx context.Context) error {
	ks, err := s.keyspace(notify.Owner)
	if err != nil {
		return err
	}
	sc, err := s.scope("notify", "instance", "class", "adapter", "topic", "severity", "result")
	if err != nil {
		return err
	}
	svc, err := notify.New(ctx, notify.Options{
		AppID:    s.in.AppID,
		Keyspace: ks,
		Catalog:  lazyCatalog{get: s.in.Catalog},
		Outbox:   s.in.Outbox,
		Registry: s.registry,
		Config: func() notify.Config {
			if s.in.Notify == nil {
				return notify.Config{}
			}
			return notifyConfig(s.in.Notify())
		},
		Metrics: sc,
		Log:     s.in.logger("notify"),
		Now:     s.in.now(),
	})
	if err != nil {
		return err
	}
	s.notify = svc
	return nil
}

// notifyConsumer is the handler core registers with the outbox under "core"
// for core.notify.deliver. It is the service's own consumer, which is
// unordered: a channel that is down must not hold another channel's entries
// behind it in the partition.
func notifyConsumer(s *integrationsServices) outbox.Handler { return s.notify.Consumer() }

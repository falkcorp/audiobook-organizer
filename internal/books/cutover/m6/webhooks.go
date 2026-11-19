// file: internal/books/cutover/m6/webhooks.go
// version: 1.0.1
// guid: 3d0c6f1e-0b6e-4f0e-8a56-6d1bb6d0a9f4
// last-edited: 2026-11-19

package m6

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/storage"
)

// ruleWebhooks is the ID of the one native rule the webhooks get (05-notify
// section 10).
const ruleWebhooks = "webhooks"

// headPlugin is HEAD's config.Plugins["webhook"] (internal/config PluginConfig),
// frozen here. Its secret is not read: M2 moved it into one store record per
// instance, and the instance refers to that record.
type headPlugin struct {
	Enabled  bool              `json:"enabled"`
	Settings map[string]string `json:"settings"`
}

// webhooks converts plugins.webhook (05-notify section 10): one instance per
// non-empty entry of the comma-separated urls (n counts the entries HEAD's
// parser keeps, from 1), and one rule over the instances that were written.
func (g *groupRun) webhooks(ctx context.Context) error {
	plugins, present := g.top["plugins"]
	if !present {
		return nil
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(plugins, &all); err != nil {
		return g.quarantineMember("plugins", plugins, reasonBlobUndecodable)
	}
	raw, present := all["webhook"]
	if !present {
		return nil
	}
	var pl headPlugin
	if err := json.Unmarshal(raw, &pl); err != nil {
		return g.quarantineMember("plugins.webhook", raw, reasonBlobUndecodable)
	}

	var refs []integrations.Ref
	n := 0
	for entry := range strings.SplitSeq(pl.Settings["urls"], ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		n++
		id, _, ok := instanceID(fmt.Sprintf("webhook-%d", n))
		if !ok {
			continue
		}
		ref := integrations.Ref{Kind: integrations.KindNotify, Instance: id}
		member := fmt.Sprintf("plugins.webhook.urls.%d", n)
		p := &pending{member: member, raw: []byte(entry), urls: map[string]string{}}
		settings := refsFor(g.set, ref)
		clean, ok := p.cleanURL("url", entry)
		if !ok {
			if err := g.quarantineMember(member, []byte(entry), reasonUnprovenURL); err != nil {
				return err
			}
			continue
		}
		settings["url"] = clean
		settings["method"] = "POST"
		p.inst = integrations.StoredInstance{Config: integrations.InstanceConfig{
			Ref: ref, Adapter: "core.webhook", Name: fmt.Sprintf("Webhook %d", n), Enabled: pl.Enabled, Settings: settings,
		}}
		if err := g.instance(ctx, p); err != nil {
			return err
		}
		if g.wrote[ref] {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return g.rule(ctx, pl.Settings["events"], refs)
}

// rule writes the rule `webhooks` over refs with the topics events maps to.
func (g *groupRun) rule(ctx context.Context, events string, refs []integrations.Ref) error {
	m, c := g.m, g.c
	var names []string
	if events == "" || events == "all" {
		names = webhookDefaultEvents
	} else {
		for e := range strings.SplitSeq(events, ",") {
			if e = strings.TrimSpace(e); e != "" {
				names = append(names, e)
			}
		}
	}
	var topics []string
	for _, e := range names {
		topic, known := webhookTopics[e]
		if !known {
			// An event outside the twelve never produced a delivery at HEAD either.
			m.report(Entry{Kind: "note", ID: "webhook-event:" + e, Detail: "unknown event, left out of the rule"})
			continue
		}
		topics = append(topics, topic)
	}
	if len(topics) == 0 {
		m.report(Entry{Kind: "note", ID: ruleWebhooks, Detail: reasonWebhookEvent})
		return nil
	}
	rule := notify.Rule{ID: ruleWebhooks, Topics: topics, MinSeverity: notify.SeverityInfo, Instances: refs, Enabled: true}
	m.report(Entry{Kind: "rule", ID: ruleWebhooks, Detail: fmt.Sprintf("%d topics, %d instances", len(topics), len(refs))})
	switch err := m.cfg.Notify.ImportRule(ctx, c.batch(), rule); {
	case err == nil:
		c.note(OutcomeRule)
		return nil
	case errors.Is(err, storage.ErrImportConflict), refused(err):
		return g.quarantineMember("plugins.webhook.events", []byte(events), reasonRefused)
	default:
		return fmt.Errorf("m6: import rule %s: %w", ruleWebhooks, err)
	}
}

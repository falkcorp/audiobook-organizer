// file: internal/plugins/webhook/plugin.go
// version: 1.0.0
// guid: f7a8b9c0-d1e2-3f4a-5b6c-7d8e9f0a1b2c

package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/plugin"
)

// Plugin delivers lifecycle events to configured webhook URLs via HTTP POST.
// It implements CapEventSubscriber: on Init it subscribes to the EventBus for
// each event type listed in its config and fires HMAC-signed HTTP POSTs.
type Plugin struct {
	urls   []string
	secret string
	events []plugin.EventType
	client *http.Client
}

func init() { plugin.Register(&Plugin{}) }

func (p *Plugin) ID() string      { return "webhook" }
func (p *Plugin) Name() string    { return "Webhook" }
func (p *Plugin) Version() string { return "1.0.0" }

func (p *Plugin) Capabilities() []plugin.Capability {
	return []plugin.Capability{plugin.CapEventSubscriber}
}

func (p *Plugin) Init(ctx context.Context, deps plugin.Deps) error {
	rawURLs := deps.Config["urls"]
	if rawURLs == "" {
		return fmt.Errorf("webhook: urls is required (comma-separated list)")
	}
	for u := range strings.SplitSeq(rawURLs, ",") {
		u = strings.TrimSpace(u)
		if u != "" {
			p.urls = append(p.urls, u)
		}
	}
	if len(p.urls) == 0 {
		return fmt.Errorf("webhook: no valid URLs configured")
	}

	p.secret = deps.Config["secret"]
	p.client = &http.Client{Timeout: 10 * time.Second}

	// Determine which events to subscribe to. Default: all.
	rawEvents := deps.Config["events"]
	if rawEvents == "" || rawEvents == "all" {
		p.events = allEventTypes()
	} else {
		for e := range strings.SplitSeq(rawEvents, ",") {
			e = strings.TrimSpace(e)
			if e != "" {
				p.events = append(p.events, plugin.EventType(e))
			}
		}
	}

	if deps.Events == nil {
		return fmt.Errorf("webhook: event bus not provided")
	}
	for _, evType := range p.events {
		et := evType // capture
		deps.Events.Subscribe(et, func(ctx context.Context, event plugin.Event) error {
			return p.deliver(ctx, event)
		})
	}

	return nil
}

func (p *Plugin) Shutdown(_ context.Context) error {
	p.urls = nil
	p.client = nil
	return nil
}

func (p *Plugin) HealthCheck() error {
	if len(p.urls) == 0 {
		return fmt.Errorf("webhook: not initialized")
	}
	return nil
}

// deliver POSTs the event as JSON to all configured URLs.
// Each request includes an X-Audiobook-Signature-256 header with an
// HMAC-SHA256 hex digest of the payload if a secret is configured.
func (p *Plugin) deliver(ctx context.Context, event plugin.Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("webhook: marshal event: %w", err)
	}

	sig := ""
	if p.secret != "" {
		mac := hmac.New(sha256.New, []byte(p.secret))
		mac.Write(body)
		sig = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}

	var errs []string
	for _, url := range p.urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", url, err))
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Audiobook-Event", string(event.Type))
		if sig != "" {
			req.Header.Set("X-Audiobook-Signature-256", sig)
		}

		resp, err := p.client.Do(req)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", url, err))
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			errs = append(errs, fmt.Sprintf("%s: HTTP %d", url, resp.StatusCode))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("webhook delivery errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func allEventTypes() []plugin.EventType {
	return []plugin.EventType{
		plugin.EventBookImported,
		plugin.EventBookDeleted,
		plugin.EventMetadataApplied,
		plugin.EventTagsWritten,
		plugin.EventFileOrganized,
		plugin.EventDedupDetected,
		plugin.EventDedupMerged,
		plugin.EventCoverChanged,
		plugin.EventReadStatusChanged,
		plugin.EventScanCompleted,
	}
}

// Compile-time check.
var _ plugin.Plugin = (*Plugin)(nil)

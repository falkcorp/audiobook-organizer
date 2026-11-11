// file: core/integrations/download/deluge/adapter.go
// version: 1.0.0
// guid: 51cd38be-8137-4a26-bb98-758d5106af77
// last-edited: 2026-11-11

// Package deluge is the Deluge adapter (core.deluge) of the download kind
// (05-download §4.1): the Web JSON-RPC client of HEAD's internal/deluge, behind
// download.Client, download.FileLister and download.Relocator.
//
// This is phase 1: list, by category (label), details and files, move, status.
// There is no add and no remove (05-download §1; HEAD's RemoveTorrent has no
// caller and is not ported). The adapter is dark until the integrations flip
// (F7-09): nothing registers it, an app does through app.Options.Adapters.
package deluge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download"
	"github.com/falkcorp/mediacore/secrets"
)

// AdapterID is the adapter's ID.
const AdapterID integrations.AdapterID = "core.deluge"

// Settings are an instance's settings (05-download §4.1): the Web UI's URL and
// the two secrets. Both secret fields are references into c/secrets/; the
// password is read when a login needs it, never at build time.
//
// DaemonPassword is declared because the spec names it (the daemon connection
// HEAD configures under download_client.torrent.deluge); phase 1 does not read
// it: the Web UI connects to its daemon with the host entry it already stores.
type Settings struct {
	URL            string      `json:"url" schema:"format=uri"`
	Password       secrets.Ref `json:"password"`
	DaemonPassword secrets.Ref `json:"daemon_password,omitzero"`
}

// Descriptor is the adapter for app.Options.Adapters. It returns an error only
// if Settings cannot be reflected, which a test of this package rules out.
func Descriptor() (integrations.Descriptor, error) {
	schema, err := config.SchemaFor[Settings]()
	if err != nil {
		return integrations.Descriptor{}, fmt.Errorf("deluge: settings schema: %w", err)
	}
	return download.Adapter(integrations.Meta{
		ID:          AdapterID,
		DisplayName: "Deluge",
		Version:     "1.0.0",
		InfoURL:     "https://deluge.readthedocs.io/en/latest/reference/webapi.html",
		Settings:    schema,
		Egress:      integrations.EgressConfigured,
		Limits:      integrations.Limits{RPS: 10, Burst: 10, Timeout: callTimeout},
	}, build), nil
}

// build makes an instance. It does no I/O: the first request logs in.
func build(_ context.Context, env integrations.Env, cfg integrations.InstanceConfig) (download.Client, error) {
	u, _ := cfg.Settings["url"].(string)
	c, err := newRPC(u, env.HTTP, env.Secrets)
	if err != nil {
		return nil, err
	}
	return &instance{rpc: c, ref: env.Ref}, nil
}

// instance is one Deluge Web UI.
type instance struct {
	rpc *rpc
	ref integrations.Ref
}

var (
	_ download.Client     = (*instance)(nil)
	_ download.FileLister = (*instance)(nil)
	_ download.Relocator  = (*instance)(nil)
	_ integrations.Tester = (*instance)(nil)
)

// Close releases nothing: the connection pool belongs to the instance's HTTP.
func (*instance) Close() error { return nil }

// Health is read-only: it logs in and asks whether the Web UI is connected to
// a daemon. A Web UI without a daemon is degraded (Test connects it); it never
// changes the remote.
func (i *instance) Health(ctx context.Context) integrations.Health {
	ok, err := i.rpc.connected(ctx)
	if err != nil {
		return i.unhealthy(ctx, err)
	}
	h := integrations.Health{Status: integrations.HealthOK}
	if !ok {
		h.Status, h.Message = integrations.HealthDegraded, "the Web UI is not connected to a daemon"
		return h
	}
	var version string
	if err := i.rpc.optional(ctx, &version, "daemon.info"); err == nil {
		h.RemoteVersion = version
	}
	return h
}

// unhealthy is the Health of a failed probe.
func (i *instance) unhealthy(ctx context.Context, err error) integrations.Health {
	e := i.fail(ctx, "health", err)
	switch integrations.ClassOf(ctx, e) {
	case integrations.ClassAuth:
		return integrations.Health{Status: integrations.HealthAuthFailed, Message: "the Web UI rejected the password"}
	case integrations.ClassRefusedLocal:
		return integrations.Health{Status: integrations.HealthMisconfigured, Message: integrations.ReasonSecretMissing}
	}
	return integrations.Health{Status: integrations.HealthDown, Message: "the Web UI did not answer usefully"}
}

// Test is the extra test of Registry.Test: connect the Web UI to the first
// daemon it lists when it has none (05-download §4.1), then fetch the caps.
func (i *instance) Test(ctx context.Context) error {
	ok, err := i.rpc.connected(ctx)
	if err != nil {
		return i.fail(ctx, "test", err)
	}
	if !ok {
		if err := i.rpc.connectFirstHost(ctx); err != nil {
			return i.fail(ctx, "test", err)
		}
		if ok, err = i.rpc.connected(ctx); err != nil {
			return i.fail(ctx, "test", err)
		} else if !ok {
			return &integrations.Error{Ref: i.ref, Op: "test", Class: integrations.ClassTransient,
				Err: errors.New("the Web UI is still not connected to a daemon")}
		}
	}
	_, err = i.Caps(ctx)
	return err
}

// Caps: torrents, files, relocation; categories when the Label plugin answers.
func (i *instance) Caps(ctx context.Context) (download.Caps, error) {
	_, ok, err := i.rpc.labels(ctx)
	if err != nil {
		return download.Caps{}, i.fail(ctx, "caps", err)
	}
	return download.Caps{Protocol: download.Torrent, Files: true, Relocate: true, Categories: ok}, nil
}

// Status reads the daemon's version, default save path, free space and labels.
// Each is best effort: a daemon that lacks one leaves it at its zero (free
// space -1), while an auth, transport or cancellation failure fails the call.
func (i *instance) Status(ctx context.Context) (download.ClientStatus, error) {
	st := download.ClientStatus{FreeSpace: -1}
	if err := i.rpc.optional(ctx, &st.Version, "daemon.info"); err != nil {
		return download.ClientStatus{}, i.fail(ctx, "status", err)
	}
	if err := i.rpc.optional(ctx, &st.DefaultSavePath, "core.get_config_value", "download_location"); err != nil {
		return download.ClientStatus{}, i.fail(ctx, "status", err)
	}
	free := int64(-1)
	if err := i.rpc.optional(ctx, &free, "core.get_free_space"); err != nil {
		return download.ClientStatus{}, i.fail(ctx, "status", err)
	}
	if free >= 0 {
		st.FreeSpace = free
	}
	labels, ok, err := i.rpc.labels(ctx)
	if err != nil {
		return download.ClientStatus{}, i.fail(ctx, "status", err)
	}
	if ok {
		st.Categories = make(map[string]string, len(labels))
		for _, l := range labels {
			st.Categories[l] = "" // the Label plugin keeps no save path
		}
	}
	return st, nil
}

// Items lists the torrents f selects, by ID. IDs go to Deluge (in chunks of
// detailChunkSize); every other field of f is applied here, so categories
// compare case-insensitively whatever the Label plugin does.
func (i *instance) Items(ctx context.Context, f download.ItemFilter) ([]download.Item, error) {
	var (
		raw map[string]torrentStatus
		err error
	)
	// Match compares IDs as given; Deluge's answer is lowercase, so the filter
	// carries the normalized ids.
	match := f
	if len(f.IDs) > 0 {
		ids := make([]string, 0, len(f.IDs))
		match.IDs = make([]download.ItemID, 0, len(f.IDs))
		for _, id := range f.IDs {
			n, err := NormalizeTorrentID(string(id))
			if err != nil {
				return nil, &integrations.Error{Ref: i.ref, Op: "items", Class: integrations.ClassInvalid, Err: err}
			}
			ids = append(ids, n)
			match.IDs = append(match.IDs, download.ItemID(n))
		}
		ids = slices.Compact(slices.Sorted(slices.Values(ids)))
		raw, err = i.rpc.statusesByID(ctx, ids)
	} else {
		raw, err = i.rpc.statuses(ctx, map[string]any{})
	}
	if err != nil {
		return nil, i.fail(ctx, "items", err)
	}
	out := make([]download.Item, 0, len(raw))
	for _, ts := range raw {
		it := i.toItem(ts)
		if match.Match(it) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Ref.ID < out[b].Ref.ID })
	return out, nil
}

// toItem normalizes one torrent (05-download §4.2).
func (i *instance) toItem(ts torrentStatus) download.Item {
	progress := min(max(ts.Progress/100, 0), 1) // Deluge reports percent
	state, seeding, unknown := mapState(ts.State, progress)
	it := download.Item{
		Ref:           download.ItemRef{Instance: i.ref, ID: download.ItemID(strings.ToLower(ts.Hash))},
		Protocol:      download.Torrent,
		Name:          ts.Name,
		Category:      ts.Label,
		State:         state,
		Seeding:       seeding,
		SeedGoalMet:   ts.IsFinished && state == download.StateCompleted,
		Progress:      progress,
		TotalSize:     max(ts.TotalSize, 0),
		RemainingSize: max(ts.TotalSize-ts.TotalDone, 0),
		Ratio:         max(ts.Ratio, 0), // Deluge reports -1 before anything is downloaded
		AddedAt:       unixTime(ts.TimeAdded),
		CompletedAt:   unixTime(ts.CompletedTime),
		RemotePath:    ts.SavePath,
		Message:       redact(ts.Message, maxMessage),
		CanMoveFiles:  state == download.StateCompleted && !seeding,
		CanBeRemoved:  state == download.StateCompleted && !seeding,
		Raw:           map[string]string{"state": redact(ts.State, maxRaw)},
	}
	if ts.ETA > 0 && ts.ETA < float64(math.MaxInt32) {
		it.ETA = time.Duration(ts.ETA * float64(time.Second))
	}
	if unknown {
		// Never Completed: a wrong Completed starts an import.
		it.Raw[download.RawUnknownState] = redact(ts.State, maxRaw)
		if it.Message == "" {
			it.Message = "unrecognized Deluge state: " + redact(ts.State, maxRaw)
		}
	}
	return it
}

// mapState is the Deluge state table of 05-download §4.2. A state that is
// complete (progress 1) but not seeding is Completed with Seeding false, which
// is what lets the importer move the files.
func mapState(s string, progress float64) (st download.State, seeding, unknown bool) {
	done := progress >= 1
	switch s {
	case "Queued":
		return download.StateQueued, false, false
	case "Checking", "Allocating", "Moving":
		if done {
			return download.StateCompleted, false, false
		}
		return download.StateDownloading, false, false
	case "Downloading":
		return download.StateDownloading, false, false
	case "Seeding":
		return download.StateCompleted, true, false
	case "Paused":
		if done {
			return download.StateCompleted, false, false
		}
		return download.StatePaused, false, false
	case "Error":
		return download.StateWarning, false, false
	}
	return download.StateWarning, false, true
}

// unixTime is Deluge's fractional Unix seconds; 0 or less is unknown.
func unixTime(sec float64) time.Time {
	if sec <= 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return time.Time{}
	}
	whole := math.Floor(sec)
	return time.Unix(int64(whole), int64((sec-whole)*1e9)).UTC()
}

// Bounds of the text an Item carries.
const (
	maxMessage = 200
	maxRaw     = 64
)

// redact makes a remote string safe to carry: URLs become "[url]" (they can
// hold tracker passkeys), control characters go, and it is cut to limit runes.
func redact(s string, limit int) string {
	fields := strings.Fields(s)
	for n, f := range fields {
		if strings.Contains(f, "://") {
			fields[n] = "[url]"
		}
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.Join(fields, " "))
	if utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit])
	}
	return s
}

// Files lists one torrent's files, Path relative to its save path.
func (i *instance) Files(ctx context.Context, id download.ItemID) ([]download.File, error) {
	n, err := NormalizeTorrentID(string(id))
	if err != nil {
		return nil, &integrations.Error{Ref: i.ref, Op: "files", Class: integrations.ClassInvalid, Err: err}
	}
	d, err := i.rpc.files(ctx, n)
	if err != nil {
		return nil, i.fail(ctx, "files", err)
	}
	out := make([]download.File, 0, len(d.Files))
	for idx, f := range d.Files {
		file := download.File{Path: f.Path, Size: f.Size}
		if idx < len(d.FileProgress) {
			file.Progress = min(max(d.FileProgress[idx], 0), 1) // file_progress is a fraction
		}
		if idx < len(d.FilePriorities) {
			file.Priority = d.FilePriorities[idx]
		}
		out = append(out, file)
	}
	return out, nil
}

// SetLocation moves the torrents' data to remoteDir on the client's disk
// (core.move_storage). Deluge moves in the background; the call returns when
// the daemon accepted it.
func (i *instance) SetLocation(ctx context.Context, ids []download.ItemID, remoteDir string) error {
	if strings.TrimSpace(remoteDir) == "" {
		return &integrations.Error{Ref: i.ref, Op: "relocate", Class: integrations.ClassInvalid, Err: errors.New("empty destination")}
	}
	norm := make([]string, 0, len(ids))
	for _, id := range ids {
		n, err := NormalizeTorrentID(string(id))
		if err != nil {
			return &integrations.Error{Ref: i.ref, Op: "relocate", Class: integrations.ClassInvalid, Err: err}
		}
		norm = append(norm, n)
	}
	if len(norm) == 0 {
		return nil
	}
	return i.fail(ctx, "relocate", i.rpc.moveStorage(ctx, norm, remoteDir))
}

// fail turns a client error into the *integrations.Error every adapter
// returns, classified (05-download §7). nil stays nil.
func (i *instance) fail(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	if existing, ok := errors.AsType[*integrations.Error](err); ok {
		if existing.Ref == (integrations.Ref{}) {
			existing.Ref = i.ref
		}
		if existing.Op == "" {
			existing.Op = op
		}
		return existing
	}
	e := &integrations.Error{Ref: i.ref, Op: op, Err: err}
	var (
		rpcErr *rpcError
		stErr  *statusError
		decErr *decodeError
		secErr *secretError
	)
	switch {
	case ctx.Err() != nil:
		e.Class = integrations.ClassCanceled
	case errors.As(err, &secErr):
		e.Class = integrations.ClassRefusedLocal
	case errors.Is(err, errLoginRejected), isAuthError(err):
		e.Class = integrations.ClassAuth
	case errors.Is(err, ErrTorrentNotFound):
		e.Class = integrations.ClassNotFound
	case errors.As(err, &rpcErr):
		e.Class, e.Status, e.Detail = integrations.ClassInvalid, rpcErr.Code, bound(redact(rpcErr.Message, integrations.MaxDetail))
	case errors.As(err, &stErr):
		e.Status, e.Detail = stErr.Status, bound(redact(stErr.Excerpt, integrations.MaxDetail))
		switch {
		case stErr.Status == http.StatusUnauthorized || stErr.Status == http.StatusForbidden:
			e.Class = integrations.ClassAuth
		case stErr.Status == http.StatusTooManyRequests:
			e.Class = integrations.ClassRateLimited
		case stErr.Status == http.StatusRequestTimeout || stErr.Status >= 500:
			e.Class = integrations.ClassTransient
		default:
			e.Class = integrations.ClassInvalid
		}
	case errors.As(err, &decErr):
		e.Class, e.Detail = integrations.ClassBadOutput, bound(redact(decErr.Excerpt, integrations.MaxDetail))
	default:
		e.Class = integrations.ClassOf(ctx, err)
	}
	return e
}

func bound(s string) string {
	if len(s) > integrations.MaxDetail {
		return s[:integrations.MaxDetail]
	}
	return s
}

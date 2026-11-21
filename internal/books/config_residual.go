// file: internal/books/config_residual.go
// version: 1.0.0
// guid: 2d7f9a41-6b83-4e05-b1c2-8a4e0d6f3c97
// last-edited: 2026-11-19

package books

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/books/appconfig"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
)

// configResidual is appconfig.Residual for books: the class-i fields of the
// rename table, which M2 leaves in the legacy settings blob (the config_blob
// row, non-secret JSON) until F7. The secrets that sit inside those values
// moved to the secret store at M2, so Load puts them back and Save takes them
// out again:
//
//   - metadata_sources[].credentials: integrations.metadata.<id>.credentials.<key>,
//     found with ListSecrets so a key HEAD has no name for is not lost;
//   - plugins.webhook.settings.secret: one record per webhook URL,
//     integrations.notify.webhook-<n>.secret, all with the same value;
//   - every other plugin's settings map: one record,
//     modules.books.plugins.<id>.settings.
//
// Only the webhook plugin keeps an entry in the blob; every other plugin is its
// settings record alone (M2 dropped the rest of the entry, enabled included, so
// Load reports such a plugin as disabled and Save refuses to enable one).
//
// What a response may carry is Redact's: credential values blanked, keys kept.
//
// A credential value in a Save is read as: a non-empty string replaces the
// secret, an empty string leaves it as it is (a client that read the redacted
// form sends it back blank), JSON null deletes it.
type configResidual struct {
	store database.SettingsStore
	acc   module.ConfigAccess
}

var _ appconfig.Residual = (*configResidual)(nil)

// residualBlobKey is the settings row that holds the blob (cutover's blobRowName).
const residualBlobKey = "config_blob"

// readBlob is the blob as a generic tree; empty when no row exists.
func (r *configResidual) readBlob() (map[string]any, error) {
	s, err := r.store.GetSetting(residualBlobKey)
	if err != nil || s == nil || s.Value == "" {
		// A store reports a missing row as an error or as nil; both mean no
		// blob, and neither is told apart from a real failure here, so a failing
		// store surfaces when the write is tried.
		return map[string]any{}, nil
	}
	return decodeTree([]byte(s.Value))
}

func decodeTree(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("the settings blob is not a JSON object: %w", err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// Load implements appconfig.Residual.
func (r *configResidual) Load(ctx context.Context, heads []string) (map[string]json.RawMessage, error) {
	blob, err := r.readBlob()
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, h := range heads {
		v, ok := getPath(blob, strings.Split(h, "."))
		if !ok || v == nil {
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("encoding %s: %w", h, err)
		}
		out[h] = raw
	}
	if want("metadata_sources", heads) {
		if v, ok := out["metadata_sources"]; ok {
			if out["metadata_sources"], err = r.withCredentials(ctx, v); err != nil {
				return nil, err
			}
		}
	}
	if want("plugins", heads) {
		base := out["plugins"]
		if base == nil {
			base = json.RawMessage(`{}`)
		}
		merged, err := r.withPluginSecrets(ctx, base)
		if err != nil {
			return nil, err
		}
		if string(merged) != "{}" {
			out["plugins"] = merged
		}
	}
	return out, nil
}

func want(h string, heads []string) bool { return slices.Contains(heads, h) }

// withCredentials returns the metadata sources list with each source's
// credentials map filled from the secret store.
func (r *configResidual) withCredentials(ctx context.Context, list json.RawMessage) (json.RawMessage, error) {
	var srcs []map[string]any
	if err := json.Unmarshal(list, &srcs); err != nil {
		return nil, fmt.Errorf("metadata_sources is not a list of objects: %w", err)
	}
	for _, src := range srcs {
		id, _ := src["id"].(string)
		if id == "" {
			continue
		}
		prefix := cutover.MetadataCredentialPrefix(id)
		metas, err := r.acc.ListSecrets(ctx, prefix)
		if err != nil {
			return nil, fmt.Errorf("listing the credentials of metadata source %s: %w", id, err)
		}
		creds, _ := src["credentials"].(map[string]any)
		if creds == nil {
			creds = map[string]any{}
		}
		for _, m := range metas {
			v, _, err := r.acc.Secret(ctx, m.Name)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", m.Name, err)
			}
			creds[cutover.MetadataCredentialKey(strings.TrimPrefix(string(m.Name), prefix))] = v.Reveal()
		}
		if len(creds) > 0 {
			src["credentials"] = creds
		}
	}
	return json.Marshal(srcs)
}

// withPluginSecrets returns the plugins map with the webhook secret and the
// other plugins' settings filled from the secret store.
func (r *configResidual) withPluginSecrets(ctx context.Context, plugins json.RawMessage) (json.RawMessage, error) {
	var m map[string]map[string]any
	if err := json.Unmarshal(plugins, &m); err != nil {
		return nil, fmt.Errorf("plugins is not an object of objects: %w", err)
	}
	if m == nil {
		m = map[string]map[string]any{}
	}
	hooks, err := r.acc.ListSecrets(ctx, cutover.WebhookSecretPrefix)
	if err != nil {
		return nil, fmt.Errorf("listing the webhook secrets: %w", err)
	}
	if len(hooks) > 0 {
		// One value for every instance; the first by name is the one to show.
		v, _, err := r.acc.Secret(ctx, hooks[0].Name)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", hooks[0].Name, err)
		}
		entry := m["webhook"]
		if entry == nil {
			entry = map[string]any{"enabled": false}
			m["webhook"] = entry
		}
		settings, _ := entry["settings"].(map[string]any)
		if settings == nil {
			settings = map[string]any{}
		}
		settings["secret"] = v.Reveal()
		entry["settings"] = settings
	}
	records, err := r.acc.ListSecrets(ctx, cutover.PluginSettingsPrefix)
	if err != nil {
		return nil, fmt.Errorf("listing the plugin settings: %w", err)
	}
	for _, meta := range records {
		id, ok := cutover.PluginIDOf(meta.Name)
		if !ok || id == "webhook" {
			continue
		}
		v, _, err := r.acc.Secret(ctx, meta.Name)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", meta.Name, err)
		}
		var settings map[string]any
		if err := json.Unmarshal([]byte(v.Reveal()), &settings); err != nil {
			return nil, fmt.Errorf("%s does not hold a settings object: %w", meta.Name, err)
		}
		if _, have := m[id]; !have {
			// M2 kept only the settings map of a plugin it does not know.
			m[id] = map[string]any{"enabled": false, "settings": settings}
		}
	}
	return json.Marshal(m)
}

// Save implements appconfig.Residual.
func (r *configResidual) Save(ctx context.Context, set map[string]json.RawMessage, unset []string) error {
	blob, err := r.readBlob()
	if err != nil {
		return err
	}
	for _, h := range slices.Sorted(maps.Keys(set)) {
		dec := json.NewDecoder(bytes.NewReader(set[h]))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("%s: %w", h, err)
		}
		switch h {
		case "metadata_sources":
			if v, err = r.splitCredentials(ctx, v); err != nil {
				return err
			}
		case "plugins":
			if v, err = r.splitPluginSecrets(ctx, v); err != nil {
				return err
			}
		}
		setPath(blob, strings.Split(h, "."), v)
	}
	for _, h := range unset {
		switch h {
		case "metadata_sources":
			if _, err := r.splitCredentials(ctx, []any{}); err != nil {
				return err
			}
		case "plugins":
			if _, err := r.splitPluginSecrets(ctx, map[string]any{}); err != nil {
				return err
			}
		}
		deletePath(blob, strings.Split(h, "."))
	}
	raw, err := json.Marshal(blob)
	if err != nil {
		return fmt.Errorf("encoding the settings blob: %w", err)
	}
	return r.store.SetSetting(residualBlobKey, string(raw), "json", false)
}

// splitCredentials moves each source's credentials into the secret store and
// returns the list without them. Sources that left the list lose theirs.
func (r *configResidual) splitCredentials(ctx context.Context, v any) (any, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, errors.New("metadata_sources must be a list")
	}
	// Everything is checked before the first write, so a refusal leaves the
	// secret store as it was.
	for i, el := range list {
		src, ok := el.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("metadata_sources[%d] must be an object", i)
		}
		id, _ := src["id"].(string)
		creds, _ := src["credentials"].(map[string]any)
		if id == "" && len(creds) > 0 {
			return nil, fmt.Errorf("metadata_sources[%d] has credentials and no id", i)
		}
		for k, c := range creds {
			switch c.(type) {
			case nil, string:
			default:
				return nil, fmt.Errorf("metadata source %s: credential %q must be a string or null", id, k)
			}
			if err := cutover.MetadataCredentialName(id, k).Validate(); err != nil {
				return nil, fmt.Errorf("metadata source %s: credential key %q cannot be a secret name: %w", id, k, err)
			}
		}
	}
	keep := map[string]bool{}
	for _, el := range list {
		src := el.(map[string]any)
		id, _ := src["id"].(string)
		creds, _ := src["credentials"].(map[string]any)
		delete(src, "credentials")
		if id == "" {
			continue
		}
		keep[id] = true
		for _, k := range slices.Sorted(maps.Keys(creds)) {
			name := cutover.MetadataCredentialName(id, k)
			switch c := creds[k].(type) {
			case nil:
				if err := r.acc.DeleteSecret(ctx, name, "config:residual"); err != nil {
					return nil, fmt.Errorf("deleting %s: %w", name, err)
				}
			case string:
				if c == "" {
					continue
				}
				if _, err := r.acc.PutSecret(ctx, name, secrets.NewValue(c), "config:residual"); err != nil {
					return nil, fmt.Errorf("storing %s: %w", name, err)
				}
			}
		}
	}
	// A source that is no longer listed takes its credentials with it.
	all, err := r.acc.ListSecrets(ctx, "integrations.metadata.")
	if err != nil {
		return nil, fmt.Errorf("listing the metadata credentials: %w", err)
	}
	for _, m := range all {
		parts := strings.Split(string(m.Name), ".")
		if len(parts) == 5 && parts[3] == "credentials" && !keep[parts[2]] {
			if err := r.acc.DeleteSecret(ctx, m.Name, "config:residual"); err != nil {
				return nil, fmt.Errorf("deleting %s: %w", m.Name, err)
			}
		}
	}
	return list, nil
}

// splitPluginSecrets moves the webhook secret and every other plugin's settings
// into the secret store and returns the plugins map with only the webhook entry
// left (without its secret).
func (r *configResidual) splitPluginSecrets(ctx context.Context, v any) (any, error) {
	plugins, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("plugins must be an object")
	}
	// Everything is checked before the first write, so a refusal leaves the
	// secret store as it was.
	for _, id := range slices.Sorted(maps.Keys(plugins)) {
		entry, ok := plugins[id].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("plugins.%s must be an object", id)
		}
		settings, _ := entry["settings"].(map[string]any)
		if id == "webhook" {
			switch settings["secret"].(type) {
			case nil, string:
			default:
				return nil, errors.New("plugins.webhook.settings.secret must be a string or null")
			}
			continue
		}
		// M2 stored nothing but the settings map of a plugin it does not know
		// (config_plan.go plugins): there is no place for its enabled flag, so a
		// write that turns one on is refused rather than reported as applied.
		if on, _ := entry["enabled"].(bool); on {
			return nil, fmt.Errorf("plugins.%s.enabled cannot be stored: only the webhook plugin keeps an enabled flag after the cutover", id)
		}
		if err := cutover.PluginSettingsName(id).Validate(); err != nil {
			return nil, fmt.Errorf("plugin id %q cannot be a secret name: %w", id, err)
		}
	}
	keepRecords := map[string]bool{}
	out := map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(plugins)) {
		entry := plugins[id].(map[string]any)
		settings, _ := entry["settings"].(map[string]any)
		if id == "webhook" {
			secret, hasSecret := settings["secret"]
			delete(settings, "secret")
			if err := r.setWebhookSecrets(ctx, settings, secret, hasSecret); err != nil {
				return nil, err
			}
			out[id] = entry
			continue
		}
		name := cutover.PluginSettingsName(id)
		if len(settings) == 0 {
			continue
		}
		raw, err := json.Marshal(settings)
		if err != nil {
			return nil, err
		}
		if _, err := r.acc.PutSecret(ctx, name, secrets.NewValue(string(raw)), "config:residual"); err != nil {
			return nil, fmt.Errorf("storing %s: %w", name, err)
		}
		keepRecords[string(name)] = true
	}
	if _, has := plugins["webhook"]; !has {
		if err := r.setWebhookSecrets(ctx, nil, nil, true); err != nil {
			return nil, err
		}
	}
	records, err := r.acc.ListSecrets(ctx, cutover.PluginSettingsPrefix)
	if err != nil {
		return nil, fmt.Errorf("listing the plugin settings: %w", err)
	}
	for _, m := range records {
		if id, ok := cutover.PluginIDOf(m.Name); ok && id != "webhook" && !keepRecords[string(m.Name)] {
			if err := r.acc.DeleteSecret(ctx, m.Name, "config:residual"); err != nil {
				return nil, fmt.Errorf("deleting %s: %w", m.Name, err)
			}
		}
	}
	return out, nil
}

// setWebhookSecrets makes the webhook instance records match the URLs: one per
// non-empty URL, n from 1, each holding the shared secret. An absent or empty
// secret keeps the value already stored (the first record's) for the instances
// that remain; null, or no webhook at all, deletes them.
func (r *configResidual) setWebhookSecrets(ctx context.Context, settings map[string]any, secret any, hasSecret bool) error {
	n := 0
	if urls, _ := settings["urls"].(string); urls != "" {
		for _, u := range strings.Split(urls, ",") {
			if strings.TrimSpace(u) != "" {
				n++
			}
		}
	}
	existing, err := r.acc.ListSecrets(ctx, cutover.WebhookSecretPrefix)
	if err != nil {
		return fmt.Errorf("listing the webhook secrets: %w", err)
	}
	value := ""
	switch s := secret.(type) {
	case string:
		value = s
	case nil:
		if hasSecret {
			n = 0 // an explicit null clears the shared secret
		}
	}
	if value == "" && n > 0 && len(existing) > 0 {
		v, _, err := r.acc.Secret(ctx, existing[0].Name)
		if err != nil {
			return fmt.Errorf("reading %s: %w", existing[0].Name, err)
		}
		value = v.Reveal()
	}
	for i := 1; i <= n && value != ""; i++ {
		if _, err := r.acc.PutSecret(ctx, cutover.WebhookSecretName(i), secrets.NewValue(value), "config:residual"); err != nil {
			return fmt.Errorf("storing %s: %w", cutover.WebhookSecretName(i), err)
		}
	}
	for _, m := range existing {
		if i := webhookIndex(m.Name); i == 0 || i > n || value == "" {
			if err := r.acc.DeleteSecret(ctx, m.Name, "config:residual"); err != nil {
				return fmt.Errorf("deleting %s: %w", m.Name, err)
			}
		}
	}
	return nil
}

// webhookIndex is n of integrations.notify.webhook-<n>.secret, 0 when name is
// not one.
func webhookIndex(name secrets.Name) int {
	mid, ok := strings.CutPrefix(string(name), cutover.WebhookSecretPrefix)
	if !ok {
		return 0
	}
	mid, ok = strings.CutSuffix(mid, ".secret")
	if !ok {
		return 0
	}
	n := 0
	for _, c := range mid {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Redact implements appconfig.Residual: credential and plugin-setting values
// blanked, their keys kept so a page can show which are set.
func (r *configResidual) Redact(head string, v json.RawMessage) json.RawMessage {
	var tree any
	if err := json.Unmarshal(v, &tree); err != nil {
		return v
	}
	blank := func(m map[string]any) {
		for k, val := range m {
			if s, ok := val.(string); ok && s != "" {
				m[k] = ""
			}
		}
	}
	switch head {
	case "metadata_sources":
		list, _ := tree.([]any)
		for _, el := range list {
			if src, ok := el.(map[string]any); ok {
				if creds, ok := src["credentials"].(map[string]any); ok {
					blank(creds)
				}
			}
		}
	case "plugins":
		m, _ := tree.(map[string]any)
		for _, el := range m {
			if entry, ok := el.(map[string]any); ok {
				if settings, ok := entry["settings"].(map[string]any); ok {
					blank(settings)
				}
			}
		}
	default:
		return v
	}
	out, err := json.Marshal(tree)
	if err != nil {
		return v
	}
	return out
}

// getPath, setPath and deletePath address a nested JSON object by its path.
func getPath(m map[string]any, segs []string) (any, bool) {
	var cur any = m
	for _, s := range segs {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[s]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func setPath(m map[string]any, segs []string, v any) {
	for _, s := range segs[:len(segs)-1] {
		next, ok := m[s].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[s] = next
		}
		m = next
	}
	m[segs[len(segs)-1]] = v
}

func deletePath(m map[string]any, segs []string) {
	for _, s := range segs[:len(segs)-1] {
		next, ok := m[s].(map[string]any)
		if !ok {
			return
		}
		m = next
	}
	delete(m, segs[len(segs)-1])
}

// file: core/kernel/core_sections_test.go
// version: 1.0.4
// guid: 9d2c7a54-61f0-4b8e-a3d5-0c1b7e6f4a92
// last-edited: 2026-11-10

package kernel

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// leaf is one declared key of a section, as the tests see it.
type leaf struct {
	path    string
	typ     reflect.Type
	reload  config.Reload
	protect config.Protection
	schema  string
	hasMap  bool // the key is a map: its entries are data, not path segments
}

// mapKeyPlaceholder stands for a map key in a leaf path of a section that is
// itself a map (core.modules, keyed by module ID), and for a list entry's ID
// in the path of that entry's secret (core.auth.oidc.<id>.client_secret).
const mapKeyPlaceholder = "<id>"

// sectionLeaves walks the section's type and returns its keys. The convention
// (names coined by F2-09): a struct field with a json name is a segment; a
// struct recurses, except secrets.Ref, which is one key (the config field
// type of a secret, 05-config §2.3; F3-03); any other type is one key, a map
// and a list included (05-config §1.3.6). The one exception is a section
// whose type is itself a map, whose entries' own keys sit under
// mapKeyPlaceholder; and a secrets.Ref field of a list entry is one more key,
// "<list parent>.<id>.<ref>" with the Ref's own tags (core/config's entry
// secrets, F3-OIDC-SECRET).
func sectionLeaves(t *testing.T, sec any) (config.Path, []leaf) {
	t.Helper()
	v := reflect.ValueOf(sec)
	path := v.MethodByName("Path").Call(nil)[0].Interface().(config.Path)
	f, ok := v.Type().FieldByName("defaults")
	if !ok {
		t.Fatalf("%T has no defaults field", sec)
	}
	var out []leaf
	walkType(t, string(path), f.Type, config.ReloadLive, config.ProtectNone, "", true, &out)
	return path, out
}

func walkType(t *testing.T, path string, typ reflect.Type, reload config.Reload, protect config.Protection, schema string, root bool, out *[]leaf) {
	t.Helper()
	switch {
	case typ == reflect.TypeFor[secrets.Ref]():
		*out = append(*out, leaf{path, typ, reload, protect, schema, false})
	case typ.Kind() == reflect.Struct:
		for f := range typ.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				t.Fatalf("%s.%s has no json name", typ, f.Name)
			}
			walkType(t, path+"."+name, f.Type, tagReload(t, f, reload), tagProtect(t, f, protect), f.Tag.Get("schema"), false, out)
		}
	case root && typ.Kind() == reflect.Map:
		walkType(t, path+"."+mapKeyPlaceholder, typ.Elem(), reload, protect, schema, false, out)
	default:
		*out = append(*out, leaf{path, typ, reload, protect, schema, typ.Kind() == reflect.Map})
		if typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.Struct {
			parent := path[:strings.LastIndex(path, ".")]
			for f := range typ.Elem().Fields() {
				if f.Type != reflect.TypeFor[secrets.Ref]() {
					continue
				}
				name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				p := parent + "." + mapKeyPlaceholder + "." + name
				*out = append(*out, leaf{p, f.Type, tagReload(t, f, config.ReloadLive), tagProtect(t, f, config.ProtectNone), "", false})
			}
		}
	}
}

// tagReload returns the field's reload class, inheriting the parent's when the
// field has no tag.
func tagReload(t *testing.T, f reflect.StructField, inherited config.Reload) config.Reload {
	t.Helper()
	switch f.Tag.Get("reload") {
	case "":
		return inherited
	case "restart":
		return config.ReloadRestart
	case "boot":
		return config.ReloadBoot
	}
	t.Fatalf("%s: bad reload tag %q", f.Name, f.Tag.Get("reload"))
	return 0
}

func tagProtect(t *testing.T, f reflect.StructField, inherited config.Protection) config.Protection {
	t.Helper()
	switch f.Tag.Get("protect") {
	case "":
		return inherited
	case "interactive":
		return config.ProtectInteractive
	case "admin":
		return config.ProtectAdmin
	case "owner":
		return config.ProtectOwner
	case "operator":
		return config.ProtectOperator
	}
	t.Fatalf("%s: bad protect tag %q", f.Name, f.Tag.Get("protect"))
	return 0
}

// allLeaves returns every key of coreSections() by path, failing on a repeat.
func allLeaves(t *testing.T) map[string]leaf {
	t.Helper()
	all := map[string]leaf{}
	for _, sec := range coreSections() {
		_, ls := sectionLeaves(t, sec)
		for _, l := range ls {
			if _, dup := all[l.path]; dup {
				t.Errorf("key %s is declared twice", l.path)
			}
			all[l.path] = l
		}
	}
	return all
}

// excluded reports the keys this row leaves to later rows, by type and prefix
// (so F2-04's additions keep the test green without editing it): a schedule
// configuration type, the core.tasks.windows keys. F3-03's secret key
// core.http.basic_gate.password and the OIDC client secret
// core.auth.oidc.<id>.client_secret are owner keys (ownerKeys).
func excluded(l leaf) bool {
	typ := l.typ
	if typ.Name() == "ScheduleConfig" && strings.HasSuffix(typ.PkgPath(), "/tasks") {
		return true
	}
	return strings.HasPrefix(l.path, "core.tasks.windows.") || l.path == "core.tasks.windows"
}

// ownerKeys is the full list of this row's key paths, transcribed from the
// owners' key tables (each table is named). A table row naming several keys
// ("a / b") is several paths. Left out: every row typed as a schedule
// configuration, the core.tasks.windows rows, the "(phase 2)" rows of
// 05-download and 05-indexer. core.shutdown.budget (05-health and 06 §6.8)
// and core.library.mappings (05-download and 05-library) appear once.
var ownerKeys = []string{
	// 05-ai §7
	"core.ai.cooldown_base", "core.ai.cooldown_max", "core.ai.quota_cooldown", "core.ai.deadline_failovers",
	"core.ai.kind_limits.text", "core.ai.kind_limits.embed", "core.ai.kind_limits.transcribe",
	"core.ai.structured.repair_attempts", "core.ai.caps_ttl", "core.ai.batch_parallelism",
	"core.ai.batch_max_apply_attempts", "core.ai.log_no_capable_every",
	// 05-authn §8
	"core.auth.mode", "core.auth.disabled_allow_remote",
	"core.auth.session.ttl", "core.auth.session.remember_ttl", "core.auth.session.idle_timeout",
	"core.auth.session.cookie_name", "core.auth.session.cookie_host_prefix",
	"core.auth.password.min_length", "core.auth.apikey.default_ttl", "core.auth.apikey.max_ttl",
	"core.auth.bootstrap.enabled", "core.auth.bootstrap.token_ttl", "core.auth.bootstrap.key_ttl",
	"core.auth.bootstrap.readonly_key.enabled", "core.auth.bootstrap.readonly_key.scopes",
	"core.auth.setup_requires_token",
	"core.auth.login.max_failures_per_source", "core.auth.login.window",
	"core.auth.login.soft_threshold", "core.auth.login.soft_step", "core.auth.login.soft_max",
	"core.auth.login.saturated_rate", "core.auth.login.saturated_burst",
	"core.auth.invite.ttl", "core.auth.temp_login.ttl",
	"core.auth.oidc.redirect_base_url", "core.auth.oidc.providers", "core.auth.oidc.<id>.client_secret",
	"core.auth.owner.required",
	// 05-backup §5B.7
	"core.backup.enabled", "core.backup.dir", "core.backup.compression", "core.backup.compression_level",
	"core.backup.include_derived", "core.backup.include_recreatable", "core.backup.verify_after_create",
	"core.backup.verify_keep", "core.backup.retention.max_count", "core.backup.retention.max_total_bytes",
	"core.backup.retention.max_age", "core.backup.before_breaking_migration", "core.backup.hash_workers",
	"core.backup.restore.rehash_on_boot",
	// 05-config §1.7 and §2.7
	"core.paths.state_dir", "core.config.file", "core.config.subscriber_drain_timeout",
	"core.secrets.key_file", "core.secrets.allow_key_regen",
	// 05-download §7
	"core.download.caps_ttl",
	// 05-events §5E.9
	"core.events.queue_size", "core.events.close_drain",
	"core.outbox.max_attempts", "core.outbox.backoff_min", "core.outbox.backoff_max",
	"core.outbox.relay_workers", "core.outbox.relay_rotate", "core.outbox.relay_enabled",
	"core.outbox.handler_timeout", "core.outbox.intake_poll", "core.outbox.unordered_parallelism",
	"core.outbox.orphan_retention",
	// 05-health §5H.7 (and 06 §6.8 for core.shutdown.budget)
	"core.health.check_interval", "core.health.check_timeout", "core.health.live_stall",
	"core.shutdown.budget", "core.shutdown.http_drain",
	"core.diagnostics.dir", "core.diagnostics.retention", "core.diagnostics.log_lines",
	"core.diagnostics.task_runs", "core.diagnostics.max_file_bytes",
	// 05-http-api §10
	"core.http.listen", "core.http.peer_allowlist", "core.http.trusted_proxies", "core.http.client_ip_header",
	"core.http.cors.allowed_origins", "core.http.body.json_max_bytes", "core.http.body.upload_max_bytes",
	"core.http.rate_limit.enabled", "core.http.rate_limit.per_minute", "core.http.rate_limit.burst",
	"core.http.rate_limit.max_clients", "core.http.basic_gate.enabled", "core.http.basic_gate.username",
	"core.http.basic_gate.password",
	"core.http.openapi.public", "core.http.read_header_timeout", "core.http.idle_timeout",
	// 05-library §5L.7 (core.library.mappings is also 05-download §7's row)
	"core.library.roots", "core.library.mappings", "core.library.protected_paths",
	"core.library.transfer.workers", "core.library.hash.max_bytes_per_sec",
	"core.library.hash.workers_per_device", "core.library.watch.debounce", "core.library.watch.poll_interval",
	"core.library.free_space.warn_percent", "core.library.free_space.default_min_bytes",
	"core.library.quarantine.retention",
	// 05-logging §8: core.log, core.log.journal
	"core.log.level", "core.log.levels", "core.log.format", "core.log.output",
	"core.log.file.path", "core.log.file.max_size_mb", "core.log.file.max_files", "core.log.file.tee_stderr",
	"core.log.add_source", "core.log.redact_keys",
	"core.log.journal.enabled", "core.log.journal.min_level", "core.log.journal.retention_days",
	"core.log.journal.queue_size",
	// 05-metadata M1.7, M2.7
	"core.integrations.egress.lan_allow", "core.integrations.health_parallelism",
	"core.integrations.probe_timeout", "core.integrations.drain_timeout",
	"core.integrations.max_instances_per_kind", "core.integrations.disabled",
	"core.providerhttp.fallback.rps", "core.providerhttp.fallback.burst",
	"core.providerhttp.fallback.max_retries", "core.providerhttp.fallback.timeout",
	"core.providerhttp.retry_after_max", "core.providerhttp.hold.max", "core.providerhttp.user_agent",
	// 05-metrics §10
	"core.metrics.prometheus.enabled", "core.metrics.path", "core.metrics.listen", "core.metrics.listen_public",
	"core.metrics.otlp.endpoint", "core.metrics.otlp.interval", "core.metrics.otlp.insecure",
	"core.metrics.environment", "core.metrics.cardinality_limit",
	"core.tracing.otlp.endpoint", "core.tracing.otlp.insecure", "core.tracing.sample_ratio",
	// 05-notify §7
	"core.notify.send_timeout", "core.notify.max_attempts", "core.notify.max_age",
	// 05-rbac §10
	"core.rbac.cache_size", "core.audit.retention.max_age", "core.audit.retention.max_records",
	"core.audit.async_buffer", "core.audit.coalesce_window", "core.audit.details_max_bytes",
	// 05-realtime §8
	"core.realtime.ring_size", "core.realtime.client_buffer", "core.realtime.max_clients",
	"core.realtime.max_per_bucket", "core.realtime.heartbeat", "core.realtime.write_timeout",
	"core.realtime.retry_after", "core.realtime.max_coalesce_keys",
	// 05-storage §5S.7
	"core.storage.path", "core.storage.cache_bytes", "core.storage.memtable_bytes",
	"core.storage.max_batch_bytes", "core.storage.close_timeout", "core.storage.snapshot_max_age",
	"core.storage.sequence.lease", "core.storage.claims.mode", "core.storage.checkpoint_dir",
	"core.storage.checkpoint.free_space_margin", "core.storage.migration.max_batch_bytes",
	"core.storage.migration.max_batch_ops", "core.storage.data_migration.chunk_bytes",
	"core.storage.data_migration.chunk_items", "core.storage.swap.intent_ttl",
	"core.storage.swap.keep_previous", "core.storage.swap.reset_hook_timeout",
	"core.index.reconcile.workers", "core.index.reconcile.interval",
	// 05-tasks §5T.10 (the windows rows and the schedule rows are F2-04's)
	"core.tasks.workers", "core.tasks.system_workers", "core.tasks.cpu_slots", "core.tasks.kinds",
	"core.tasks.shutdown_drain", "core.tasks.shutdown_extend_max", "core.tasks.min_checkpoint_interval",
	"core.tasks.max_yields", "core.tasks.zombie_cap", "core.tasks.standdown_lease",
	"core.tasks.schedule_poll", "core.tasks.timezone", "core.tasks.idempotency_ttl",
	"core.tasks.history_retention",
	// 05-updater §5U.8
	"core.updater.mode", "core.updater.channel", "core.updater.max_download",
	// 06 §6.8: core.modules.<id>.enabled, core.lifecycle
	"core.modules.<id>.enabled", "core.lifecycle.start_timeout", "core.lifecycle.restart_exit_code",
	// 07 §7.15
	"core.ui.enabled", "core.ui.csp.connect_src", "core.ui.csp.report_only", "core.ui.diagnostics.accept",
}

// TestCoreSections_EveryOwnerKeyDeclared is a test name coined by F2-09.
func TestCoreSections_EveryOwnerKeyDeclared(t *testing.T) {
	got := []string{}
	for p, l := range allLeaves(t) {
		if !excluded(l) {
			got = append(got, p)
		}
	}
	sort.Strings(got)
	want := slices.Clone(ownerKeys)
	sort.Strings(want)
	if dups := duplicates(want); len(dups) > 0 {
		t.Fatalf("ownerKeys lists %v twice", dups)
	}
	for _, p := range want {
		if _, i := slices.BinarySearch(got, p); !i {
			t.Errorf("owner key %s is not declared", p)
		}
	}
	for _, p := range got {
		if _, i := slices.BinarySearch(want, p); !i {
			t.Errorf("declared key %s is in no owner table of this row", p)
		}
	}
	if len(got) != len(want) {
		t.Errorf("declared %d keys, owner tables list %d", len(got), len(want))
	}
}

func duplicates(sorted []string) []string {
	var d []string
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			d = append(d, sorted[i])
		}
	}
	return d
}

// TestCoreSections_PathsValid is a test name coined by F2-09: every section
// path and every key path passes Path.Validate, map keys excepted.
func TestCoreSections_PathsValid(t *testing.T) {
	for _, sec := range coreSections() {
		path, leaves := sectionLeaves(t, sec)
		if err := path.Validate(); err != nil {
			t.Errorf("section %s: %v", path, err)
		}
		if !strings.HasPrefix(string(path), "core.") {
			t.Errorf("section %s is not a core section", path)
		}
		for _, l := range leaves {
			p := strings.ReplaceAll(l.path, "."+mapKeyPlaceholder, ".x")
			if err := config.Path(p).Validate(); err != nil {
				t.Errorf("key %s: %v", l.path, err)
			}
		}
	}
}

// TestCoreSections_NoOverlap is a test name coined by F2-09: no two sections
// share a path, and a section nested under another (core.log.journal under
// core.log) sits at a segment the parent's type does not declare.
func TestCoreSections_NoOverlap(t *testing.T) {
	paths := map[string][]leaf{}
	for _, sec := range coreSections() {
		p, ls := sectionLeaves(t, sec)
		if _, dup := paths[string(p)]; dup {
			t.Errorf("section %s is declared twice", p)
		}
		paths[string(p)] = ls
	}
	for child := range paths {
		for parent, ls := range paths {
			seg, ok := strings.CutPrefix(child, parent+".")
			if !ok {
				continue
			}
			seg, _, _ = strings.Cut(seg, ".")
			for _, l := range ls {
				if strings.HasPrefix(l.path, parent+"."+seg+".") || l.path == parent+"."+seg {
					t.Errorf("section %s overlaps %s: the parent declares %q", child, parent, seg)
				}
			}
		}
	}
	if len(paths["core.log"]) == 0 || len(paths["core.log.journal"]) == 0 {
		t.Error("the core.log / core.log.journal pair is not exercised")
	}
}

// TestCoreSections_NoLaterRowsKeys is a test name coined by F2-09: nothing the
// brief leaves to F2-04 and F3-02 is declared here. F3-03's secret keys,
// core.http.basic_gate.password and the OIDC client secret (a list entry's,
// F3-OIDC-SECRET), are both declared.
func TestCoreSections_NoLaterRowsKeys(t *testing.T) {
	for p, l := range allLeaves(t) {
		if excluded(l) {
			t.Errorf("key %s belongs to a later row", p)
		}
		if strings.Contains(p, ".schedules.") {
			t.Errorf("key %s belongs to a later row", p)
		}
	}
}

// TestCoreSections_SecretKeys (a test name coined by F3-03; 05-config §2.2,
// §1.12.3, 05-http-api §10, 05-authn §8): the secrets.Ref keys are
// core.http.basic_gate.password and each OIDC provider entry's
// core.auth.oidc.<id>.client_secret, both live and ProtectAdmin (the client
// secret by its own tag, not its owner list's); no other key is a secret.
func TestCoreSections_SecretKeys(t *testing.T) {
	refType := reflect.TypeFor[secrets.Ref]()
	all := allLeaves(t)
	var refs []string
	for p, l := range all {
		if l.typ == refType {
			refs = append(refs, p)
		}
	}
	slices.Sort(refs)
	want := []string{"core.auth.oidc.<id>.client_secret", "core.http.basic_gate.password"}
	if !slices.Equal(refs, want) {
		t.Fatalf("secret keys %v, want %v", refs, want)
	}
	for _, p := range want {
		if l := all[p]; l.reload != config.ReloadLive || l.protect != config.ProtectAdmin {
			t.Errorf("%s: reload %d protect %d, want live and ProtectAdmin", p, l.reload, l.protect)
		}
	}
	if l := all["core.auth.oidc.providers"]; l.protect != config.ProtectOwner {
		t.Errorf("core.auth.oidc.providers: protect %d, want ProtectOwner", l.protect)
	}
}

var schemaFormats = []string{"duration", "uri", "cidr", "path", "email"}

// TestCoreSections_TagVocabulary is a test name coined by F2-09: every schema
// tag uses the keywords of 05-config §1.3.1, and every json name is a valid
// path segment.
func TestCoreSections_TagVocabulary(t *testing.T) {
	for p, l := range allLeaves(t) {
		if l.schema == "" {
			continue
		}
		for _, kw := range splitSchema(l.schema) {
			k, v, ok := strings.Cut(kw, "=")
			if !ok || v == "" {
				t.Errorf("%s: schema keyword %q has no value", p, kw)
				continue
			}
			switch k {
			case "minimum", "maximum", "pattern":
			case "enum":
				if len(strings.Split(v, "|")) < 2 {
					t.Errorf("%s: enum %q has fewer than two values", p, v)
				}
			case "format":
				if !slices.Contains(schemaFormats, v) {
					t.Errorf("%s: unknown format %q", p, v)
				}
			default:
				t.Errorf("%s: unknown schema keyword %q", p, k)
			}
		}
	}
}

// splitSchema splits a schema tag on commas that are not inside a value.
func splitSchema(s string) []string { return strings.Split(s, ",") }

// TestCoreSections_ReloadAndProtection is a test name coined by F2-09. It
// checks the reload and protection of keys whose rule the brief names, and the
// storage section's table-wide rule (every key ProtectOperator).
func TestCoreSections_ReloadAndProtection(t *testing.T) {
	all := allLeaves(t)
	type want struct {
		reload  config.Reload
		protect config.Protection
	}
	cases := map[string]want{
		"core.paths.state_dir":               {config.ReloadBoot, config.ProtectOperator},
		"core.config.file":                   {config.ReloadRestart, config.ProtectOperator},
		"core.backup.dir":                    {config.ReloadRestart, config.ProtectAdmin},
		"core.modules.<id>.enabled":          {config.ReloadBoot, config.ProtectNone},
		"core.auth.mode":                     {config.ReloadRestart, config.ProtectOwner},
		"core.auth.owner.required":           {config.ReloadRestart, config.ProtectOwner},
		"core.auth.session.ttl":              {config.ReloadLive, config.ProtectNone},
		"core.auth.login.saturated_rate":     {config.ReloadRestart, config.ProtectAdmin},
		"core.backup.verify_after_create":    {config.ReloadLive, config.ProtectInteractive},
		"core.library.mappings":              {config.ReloadLive, config.ProtectInteractive},
		"core.library.roots":                 {config.ReloadLive, config.ProtectAdmin},
		"core.http.trusted_proxies":          {config.ReloadLive, config.ProtectAdmin},
		"core.http.basic_gate.enabled":       {config.ReloadLive, config.ProtectAdmin},
		"core.http.basic_gate.password":      {config.ReloadLive, config.ProtectAdmin},
		"core.log.file.path":                 {config.ReloadRestart, config.ProtectAdmin},
		"core.log.file.max_files":            {config.ReloadRestart, config.ProtectNone},
		"core.log.journal.enabled":           {config.ReloadRestart, config.ProtectNone},
		"core.metrics.listen_public":         {config.ReloadRestart, config.ProtectOperator},
		"core.shutdown.budget":               {config.ReloadRestart, config.ProtectNone},
		"core.updater.mode":                  {config.ReloadLive, config.ProtectAdmin},
		"core.lifecycle.restart_exit_code":   {config.ReloadLive, config.ProtectAdmin},
		"core.integrations.egress.lan_allow": {config.ReloadLive, config.ProtectAdmin},
		"core.audit.retention.max_age":       {config.ReloadLive, config.ProtectAdmin},
		"core.tasks.kinds":                   {config.ReloadLive, config.ProtectNone},
		"core.tasks.workers":                 {config.ReloadRestart, config.ProtectNone},
		"core.realtime.max_clients":          {config.ReloadLive, config.ProtectNone},
		"core.storage.max_batch_bytes":       {config.ReloadLive, config.ProtectOperator},
		"core.storage.cache_bytes":           {config.ReloadRestart, config.ProtectOperator},
		"core.index.reconcile.workers":       {config.ReloadLive, config.ProtectOperator},
		"core.ui.enabled":                    {config.ReloadRestart, config.ProtectNone},
		"core.ui.csp.connect_src":            {config.ReloadLive, config.ProtectNone},
		"core.secrets.allow_key_regen":       {config.ReloadRestart, config.ProtectOperator},
		"core.auth.oidc.providers":           {config.ReloadLive, config.ProtectOwner},
		"core.diagnostics.dir":               {config.ReloadRestart, config.ProtectAdmin},
		"core.http.rate_limit.max_clients":   {config.ReloadRestart, config.ProtectNone},
	}
	for p, w := range cases {
		l, ok := all[p]
		if !ok {
			t.Errorf("%s is not declared", p)
			continue
		}
		if l.reload != w.reload || l.protect != w.protect {
			t.Errorf("%s: reload %d protect %d, want %d %d", p, l.reload, l.protect, w.reload, w.protect)
		}
	}
	for p, l := range all {
		if (strings.HasPrefix(p, "core.storage.") || strings.HasPrefix(p, "core.index.")) && l.protect != config.ProtectOperator {
			t.Errorf("%s: protect %d, want ProtectOperator (05-storage §5S.7: every key)", p, l.protect)
		}
		if l.reload == config.ReloadBoot && p != "core.paths.state_dir" && !strings.HasPrefix(p, "core.modules.") {
			t.Errorf("%s is boot-bound; only the state dir and module enablement are", p)
		}
	}
}

// defaultValue returns the declared default of one key as a value the tests
// can compare: durations as time.Duration, others by kind.
func defaultValue(t *testing.T, sec any, path string) any {
	t.Helper()
	v := reflect.ValueOf(sec)
	secPath := string(v.MethodByName("Path").Call(nil)[0].Interface().(config.Path))
	d := v.FieldByName("defaults")
	rest := strings.TrimPrefix(path, secPath+".")
	if d.Kind() == reflect.Map {
		t.Fatalf("defaultValue does not read into the map section %s", secPath)
	}
	for seg := range strings.SplitSeq(rest, ".") {
		found := false
		for i := 0; i < d.NumField(); i++ {
			name, _, _ := strings.Cut(d.Type().Field(i).Tag.Get("json"), ",")
			if name == seg {
				d, found = d.Field(i), true
				break
			}
		}
		if !found {
			t.Fatalf("%s: no field %q", path, seg)
		}
	}
	switch {
	case d.Type() == reflect.TypeFor[config.Duration]():
		return time.Duration(d.Int())
	}
	switch d.Kind() {
	case reflect.Bool:
		return d.Bool()
	case reflect.String:
		return d.String()
	case reflect.Int, reflect.Int64:
		return d.Int()
	case reflect.Float64:
		return d.Float()
	case reflect.Slice, reflect.Map:
		return d.Len()
	}
	t.Fatalf("%s: unsupported kind %s", path, d.Kind())
	return nil
}

// TestCoreSections_Defaults is a test name coined by F2-09. Spot checks of the
// Default column, one or more per owner table; host-dependent defaults are
// checked against their formula.
func TestCoreSections_Defaults(t *testing.T) {
	byPath := map[string]any{}
	for _, sec := range coreSections() {
		byPath[string(reflect.ValueOf(sec).MethodByName("Path").Call(nil)[0].Interface().(config.Path))] = sec
	}
	get := func(section, path string) any {
		t.Helper()
		s, ok := byPath[section]
		if !ok {
			t.Fatalf("no section %s", section)
		}
		return defaultValue(t, s, path)
	}
	type c struct {
		section, path string
		want          any
	}
	cases := []c{
		{"core.ai", "core.ai.cooldown_base", 30 * time.Second},
		{"core.ai", "core.ai.quota_cooldown", 30 * time.Minute},
		{"core.ai", "core.ai.batch_parallelism", int64(2)},
		{"core.ai", "core.ai.kind_limits.embed", int64(0)},
		{"core.auth", "core.auth.mode", "required"},
		{"core.auth", "core.auth.session.ttl", 24 * time.Hour},
		{"core.auth", "core.auth.session.remember_ttl", 168 * time.Hour},
		{"core.auth", "core.auth.apikey.max_ttl", 8760 * time.Hour},
		{"core.auth", "core.auth.bootstrap.key_ttl", 8 * time.Hour},
		{"core.auth", "core.auth.bootstrap.readonly_key.scopes", 1},
		{"core.auth", "core.auth.login.soft_step", 200 * time.Millisecond},
		{"core.auth", "core.auth.login.saturated_rate", float64(2)},
		{"core.auth", "core.auth.login.saturated_burst", int64(8)},
		{"core.auth", "core.auth.setup_requires_token", true},
		{"core.auth", "core.auth.disabled_allow_remote", false},
		{"core.backup", "core.backup.enabled", true},
		{"core.backup", "core.backup.dir", "backups"},
		{"core.backup", "core.backup.compression", "zstd"},
		{"core.backup", "core.backup.retention.max_count", int64(10)},
		{"core.backup", "core.backup.retention.max_total_bytes", int64(40 << 30)},
		{"core.paths", "core.paths.state_dir", ""},
		{"core.config", "core.config.subscriber_drain_timeout", 5 * time.Second},
		{"core.secrets", "core.secrets.key_file", "secrets.keyring"},
		{"core.download", "core.download.caps_ttl", time.Hour},
		{"core.events", "core.events.queue_size", int64(256)},
		{"core.outbox", "core.outbox.max_attempts", int64(20)},
		{"core.outbox", "core.outbox.orphan_retention", 7 * 24 * time.Hour},
		{"core.shutdown", "core.shutdown.budget", 25 * time.Second},
		{"core.diagnostics", "core.diagnostics.max_file_bytes", int64(64 << 20)},
		{"core.http", "core.http.listen", ":8484"},
		{"core.http", "core.http.body.json_max_bytes", int64(1 << 20)},
		{"core.http", "core.http.body.upload_max_bytes", int64(10 << 20)},
		{"core.http", "core.http.rate_limit.per_minute", int64(100)},
		{"core.http", "core.http.rate_limit.burst", int64(max(100/5, 10))},
		{"core.http", "core.http.basic_gate.enabled", false},
		{"core.http", "core.http.idle_timeout", 120 * time.Second},
		{"core.library", "core.library.free_space.default_min_bytes", int64(5 << 30)},
		{"core.library", "core.library.watch.poll_interval", 15 * time.Minute},
		{"core.log", "core.log.level", "info"},
		{"core.log", "core.log.format", "text"},
		{"core.log", "core.log.file.max_size_mb", int64(100)},
		{"core.log", "core.log.file.max_files", int64(7)},
		{"core.log.journal", "core.log.journal.retention_days", int64(90)},
		{"core.log.journal", "core.log.journal.queue_size", int64(4096)},
		{"core.integrations", "core.integrations.max_instances_per_kind", int64(32)},
		{"core.providerhttp", "core.providerhttp.fallback.rps", 2.0},
		{"core.providerhttp", "core.providerhttp.hold.max", 24 * time.Hour},
		{"core.metrics", "core.metrics.path", "/metrics"},
		{"core.metrics", "core.metrics.otlp.interval", 60 * time.Second},
		{"core.metrics", "core.metrics.environment", "prod"},
		{"core.metrics", "core.metrics.cardinality_limit", int64(2000)},
		{"core.tracing", "core.tracing.sample_ratio", 1.0},
		{"core.notify", "core.notify.max_attempts", int64(10)},
		{"core.rbac", "core.rbac.cache_size", int64(4096)},
		{"core.audit", "core.audit.retention.max_records", int64(1000000)},
		{"core.realtime", "core.realtime.ring_size", int64(1024)},
		{"core.realtime", "core.realtime.max_coalesce_keys", int64(10000)},
		{"core.storage", "core.storage.cache_bytes", int64(512 << 20)},
		{"core.storage", "core.storage.claims.mode", "strict"},
		{"core.storage", "core.storage.checkpoint.free_space_margin", int64(2 << 30)},
		{"core.storage", "core.storage.migration.max_batch_ops", int64(250000)},
		{"core.storage", "core.storage.swap.keep_previous", 168 * time.Hour},
		{"core.storage", "core.storage.swap.reset_hook_timeout", 30 * time.Minute},
		{"core.index", "core.index.reconcile.interval", 5 * time.Second},
		{"core.tasks", "core.tasks.workers", int64(8)},
		{"core.tasks", "core.tasks.system_workers", int64(2)},
		{"core.tasks", "core.tasks.zombie_cap", int64(4)},
		{"core.tasks", "core.tasks.history_retention", 90 * 24 * time.Hour},
		{"core.tasks", "core.tasks.idempotency_ttl", 7 * 24 * time.Hour},
		{"core.tasks", "core.tasks.kinds", 0},
		{"core.updater", "core.updater.mode", "off"},
		{"core.updater", "core.updater.channel", "stable"},
		{"core.updater", "core.updater.max_download", int64(512 << 20)},
		{"core.lifecycle", "core.lifecycle.restart_exit_code", int64(75)},
		{"core.lifecycle", "core.lifecycle.start_timeout", 60 * time.Second},
		{"core.ui", "core.ui.enabled", true},
		{"core.ui", "core.ui.diagnostics.accept", true},
	}
	for _, tc := range cases {
		if got := get(tc.section, tc.path); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s default = %v (%T), want %v (%T)", tc.path, got, got, tc.want, tc.want)
		}
	}
	// Host-dependent defaults follow the owner tables' formulas.
	hash := int64(hashWorkersDefault())
	if hash < 1 || hash > 4 {
		t.Errorf("hash worker default %d is outside [1,4]", hash)
	}
	for _, p := range [][2]string{{"core.backup", "core.backup.hash_workers"}, {"core.library", "core.library.hash.workers_per_device"}} {
		if got := get(p[0], p[1]); got != hash {
			t.Errorf("%s default = %v, want %d", p[1], got, hash)
		}
	}
}

// TestCoreSections_JSONShape is a test name coined by F2-09: every section's
// type marshals to JSON and decodes back strictly (so every json name is
// unique within its struct), and every
// list and map default is an empty value, not nil, so it reads as [] or {} and
// not null (the owner tables' "[]" and "{}" defaults). The map section
// core.modules is the one nil default: each manifest's DefaultEnabled applies.
func TestCoreSections_JSONShape(t *testing.T) {
	for _, sec := range coreSections() {
		v := reflect.ValueOf(sec)
		path := string(v.MethodByName("Path").Call(nil)[0].Interface().(config.Path))
		ft, _ := v.Type().FieldByName("defaults")
		zero := reflect.New(ft.Type)
		b, err := json.Marshal(zero.Interface())
		if err != nil {
			t.Errorf("%s: marshal: %v", path, err)
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(reflect.New(ft.Type).Interface()); err != nil {
			t.Errorf("%s: strict decode of %s: %v", path, b, err)
		}
		if ft.Type.Kind() == reflect.Map {
			continue
		}
		var walk func(p string, d reflect.Value)
		walk = func(p string, d reflect.Value) {
			switch d.Kind() {
			case reflect.Struct:
				for i := 0; i < d.NumField(); i++ {
					name, _, _ := strings.Cut(d.Type().Field(i).Tag.Get("json"), ",")
					walk(p+"."+name, d.Field(i))
				}
			case reflect.Slice, reflect.Map:
				if d.IsNil() {
					t.Errorf("%s: default is nil, want an empty value", p)
				}
			}
		}
		walk(path, v.FieldByName("defaults"))
	}
}

// TestCoreSections_CoreModulesIsKeyedByModuleID is a test name coined by
// F2-09: core.modules is a map section whose entries hold one boot-bound key,
// and its default is the empty map (each manifest's DefaultEnabled applies).
func TestCoreSections_CoreModulesIsKeyedByModuleID(t *testing.T) {
	all := allLeaves(t)
	var under []string
	for p := range all {
		if strings.HasPrefix(p, "core.modules.") {
			under = append(under, p)
		}
	}
	if !slices.Equal(under, []string{"core.modules.<id>.enabled"}) {
		t.Fatalf("keys under core.modules = %v", under)
	}
	if all["core.modules.<id>.enabled"].typ.Kind() != reflect.Bool {
		t.Error("core.modules.<id>.enabled is not a bool")
	}
}

// TestCoreSections_OIDCClientSecretPerProvider (F3-OIDC-SECRET; 05-authn §8,
// 05-config §1.12.3): over the kernel's config store, each provider entry
// gets its own secret key core.auth.oidc.<id>.client_secret, live and
// ProtectAdmin, written by an admin without owner proof; an entry whose id
// would collide with a sibling key is refused; removing an entry deletes its
// secret in the same commit.
func TestCoreSections_OIDCClientSecretPerProvider(t *testing.T) {
	in := configIn(t)
	in.OwnerSet = func() bool { return true }
	cb, cm := startedCfg(t, in)
	proof := interactiveAdmin()
	proof.Owner = true
	const github, google = config.Path("core.auth.oidc.github.client_secret"), config.Path("core.auth.oidc.google.client_secret")
	two := `[{"id":"github","kind":"github","issuer":"","client_id":"bat","scopes":[],"allowed_emails":["bruce@example.com"],"allowed_domains":[],"default_role":"viewer","link_existing_by_email":true},` +
		`{"id":"google","kind":"oidc","issuer":"accounts.google.com","client_id":"robin","scopes":[],"allowed_emails":["bruce@example.com"],"allowed_domains":[],"default_role":"viewer","link_existing_by_email":true}]`
	if err := applyOne(cb, proof, "core.auth.oidc.providers", two); err != nil {
		t.Fatal(err)
	}
	for _, p := range []config.Path{github, google} {
		ks, ok := cb.store.Lookup(p)
		if !ok || ks.Secret == nil || ks.Effective != nil || ks.Protection != config.ProtectAdmin || ks.Reload != config.ReloadLive || ks.Section != "core.auth" {
			t.Fatalf("%s: %+v (%v)", p, ks, ok)
		}
	}
	if err := applyOne(cb, interactiveAdmin(), github, `"wayne-tech"`); err != nil {
		t.Fatalf("an admin without owner proof sets a client secret: %v", err)
	}
	if w := cm.Commits()[len(cm.Commits())-1].Secrets; len(w) != 1 || w[0].Name != secrets.Name(github) || w[0].Value.Reveal() != "wayne-tech" {
		t.Errorf("writes %+v", w)
	}
	for _, id := range []string{"providers", "redirect_base_url", "Git-Hub", ""} {
		v := `[{"id":"` + id + `","kind":"github","issuer":"","client_id":"bat","scopes":[],"allowed_emails":[],"allowed_domains":[],"default_role":"viewer","link_existing_by_email":false}]`
		if err := applyOne(cb, proof, "core.auth.oidc.providers", v); !errors.Is(err, config.ErrInvalid) {
			t.Errorf("id %q: %v, want ErrInvalid", id, err)
		}
	}
	one := `[` + two[1:strings.Index(two, "},{")+1] + `]`
	if err := applyOne(cb, proof, "core.auth.oidc.providers", one); err != nil {
		t.Fatal(err)
	}
	if w := cm.Commits()[len(cm.Commits())-1].Secrets; len(w) != 1 || w[0].Name != secrets.Name(google) || !w[0].Delete {
		t.Errorf("removing google writes %+v", w)
	}
	if _, ok := cb.store.Lookup(google); ok {
		t.Error("a removed provider's client secret is still a key")
	}
	// A kept provider keeps its client secret through a change to who may
	// sign in, and loses it to a change of provider or client (a rebind).
	calls := len(cm.Commits())
	cur := strings.Replace(one, `"allowed_emails":["bruce@example.com"]`, `"allowed_emails":["alfred@example.com"]`, 1)
	if err := applyOne(cb, proof, "core.auth.oidc.providers", cur); err != nil {
		t.Fatal(err)
	}
	if len(cm.Commits()) != calls+1 || len(cm.Commits()[calls].Secrets) != 0 {
		t.Errorf("an allowed_emails change writes %+v", cm.Commits()[len(cm.Commits())-1].Secrets)
	}
	for _, swap := range [][2]string{
		{`"client_id":"bat"`, `"client_id":"joker"`},
		{`"issuer":""`, `"issuer":"https://idp.example.org"`},
		{`"kind":"github"`, `"kind":"oidc"`},
	} {
		next := strings.Replace(cur, swap[0], swap[1], 1)
		if next == cur {
			t.Fatalf("%s is not in %s", swap[0], cur)
		}
		cur = next
		if err := applyOne(cb, proof, "core.auth.oidc.providers", cur); err != nil {
			t.Fatalf("%s: %v", swap[1], err)
		}
		if w := cm.Commits()[len(cm.Commits())-1].Secrets; len(w) != 1 || w[0].Name != secrets.Name(github) || !w[0].Delete {
			t.Errorf("%s writes %+v, want the client secret deleted", swap[1], w)
		}
	}
}

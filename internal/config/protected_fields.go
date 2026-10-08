// file: internal/config/protected_fields.go
// version: 1.1.0
// guid: 7c41d2e8-5a96-4b3f-8e17-2f0b9d6a4c53
// last-edited: 2026-10-07

package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// FieldClass says why a config field may or may not be changed by a caller
// that is not an interactive session (an API key, an ABS token).
type FieldClass int

const (
	// FieldUnprotected may be changed by any caller with settings.manage.
	FieldUnprotected FieldClass = iota
	// FieldSignIn decides who can sign in and with what. Changing it is a
	// credential change: with oauth_allowed_emails and oauth_default_role
	// alone, a key could let an email it controls sign in as a new admin.
	FieldSignIn
	// FieldExecutable names a program the server runs, or decides whether
	// the server runs its own managed copy or a path the caller names.
	FieldExecutable
	// FieldDatabase names where the server keeps its databases.
	FieldDatabase
	// FieldServerPath names a path the server reads, writes, scans or
	// serves, or decides which of several such paths it uses.
	FieldServerPath
)

func (c FieldClass) String() string {
	switch c {
	case FieldSignIn:
		return "sign-in setting"
	case FieldExecutable:
		return "executable"
	case FieldDatabase:
		return "database location"
	case FieldServerPath:
		return "server path"
	default:
		return "unprotected"
	}
}

// Protected reports whether a field of this class needs an interactive
// session to change.
func (c FieldClass) Protected() bool { return c != FieldUnprotected }

type fieldRule struct {
	Class FieldClass
	Why   string
}

// configFieldRules is THE classification of config fields that the
// interactive-session rule cares about. It is the only list: UpdateService
// enforces it (ChangedProtectedFields), and protected_fields_test.go fails
// when a field whose name looks like a path, an executable, a URL or a
// sign-in setting exists in Config without an entry here.
//
// Keys are JSON paths. "[]" stands for every element of a slice or every
// value of a map; a key covers every field below it.
var configFieldRules = map[string]fieldRule{
	// ---- sign-in settings ----
	"enable_auth":                {FieldSignIn, "turns authentication off for everyone"},
	"basic_auth_enabled":         {FieldSignIn, "enables a shared username/password sign-in"},
	"basic_auth_username":        {FieldSignIn, "the shared sign-in's username"},
	"basic_auth_password":        {FieldSignIn, "the shared sign-in's password"},
	"oauth_enabled":              {FieldSignIn, "enables SSO sign-in"},
	"oauth_github_client_id":     {FieldSignIn, "points SSO at an OAuth app the caller may control"},
	"oauth_github_client_secret": {FieldSignIn, "SSO app secret"},
	"oauth_google_client_id":     {FieldSignIn, "points SSO at an OAuth app the caller may control"},
	"oauth_google_client_secret": {FieldSignIn, "SSO app secret"},
	"oauth_redirect_base_url":    {FieldSignIn, "where the SSO code is sent back to"},
	"oauth_allowed_emails":       {FieldSignIn, "who may sign in through SSO"},
	"oauth_default_role":         {FieldSignIn, "the role a new SSO user gets (admin would mint admins)"},
	"cf_access_team_domain":      {FieldSignIn, "which Cloudflare Access team's JWTs are trusted"},
	"cf_access_aud":              {FieldSignIn, "which Cloudflare Access application's JWTs are trusted"},
	"owner_email":                {FieldSignIn, "the Cloudflare Access identity owner-only actions require"},
	"abs_api_enabled":            {FieldSignIn, "enables the Audiobookshelf-compatible token sign-in"},
	"abs_auth_modes":             {FieldSignIn, "which ABS sign-in modes are accepted"},
	"abs_access_token_ttl":       {FieldSignIn, "lifetime of ABS bearer tokens"},
	"abs_refresh_token_ttl":      {FieldSignIn, "lifetime of ABS refresh tokens"},
	"abs_refresh_grace":          {FieldSignIn, "how long a used ABS refresh token keeps working"},
	"write_startup_readonly_key": {FieldSignIn, "mints an API key at every start"},
	"enable_rate_limit":          {FieldSignIn, "turning it off removes the brute-force limit on sign-in"},
	"auth_rate_limit_per_minute": {FieldSignIn, "the brute-force limit on sign-in"},

	// ---- executables ----
	"tools.managed_dir":        {FieldExecutable, "directory the server installs and runs managed tools from"},
	"tools.ollama.mode":        {FieldExecutable, "chooses between the managed binary and custom_path"},
	"tools.ollama.custom_path": {FieldExecutable, "a program the server executes"},
	"tools.fpcalc.mode":        {FieldExecutable, "chooses between the managed binary and custom_path"},
	"tools.fpcalc.custom_path": {FieldExecutable, "a program the server executes"},

	// ---- database location ----
	"database_path":              {FieldDatabase, "the main database"},
	"database_type":              {FieldDatabase, "immutable at runtime; refused outright as well"},
	"activity_backend":           {FieldDatabase, "chooses which activity store (and file) is used"},
	"activity_db_path":           {FieldDatabase, "the activity database"},
	"activity_db_move_on_change": {FieldDatabase, "moves the activity database to a new path"},

	// ---- server paths ----
	"root_dir":                       {FieldServerPath, "library root the server organizes into, scans and serves"},
	"path_aliases":                   {FieldServerPath, "rewrites stored paths to server paths"},
	"playlist_dir":                   {FieldServerPath, "directory the server writes playlists to"},
	"backup_dir":                     {FieldServerPath, "directory backups are written to and restored from"},
	"openlibrary_dump_dir":           {FieldServerPath, "directory the server reads dumps from"},
	"whisper_clip_cache_dir":         {FieldServerPath, "directory the server writes clips to"},
	"folder_naming_pattern":          {FieldServerPath, "decides where organize writes files"},
	"file_naming_pattern":            {FieldServerPath, "decides where organize writes files"},
	"protected_paths":                {FieldServerPath, "paths the server must never write or delete"},
	"itunes.library_write_path":      {FieldServerPath, "iTunes library (.itl) file the server reads and protects (never writes)"},
	"itunes.library_read_path":       {FieldServerPath, "iTunes library file the server reads"},
	"itunes.windows_root_path":       {FieldServerPath, "maps iTunes paths to server paths"},
	"itunes.media_root":              {FieldServerPath, "maps iTunes paths to server paths"},
	"itunes.path_mappings":           {FieldServerPath, "maps iTunes paths to server paths"},
	"itunes.libraries.original":      {FieldServerPath, "iTunes library files the server reads and writes"},
	"itunes.libraries.ao":            {FieldServerPath, "iTunes library files the server reads and writes"},
	"itunes.libraries.pointed_at":    {FieldServerPath, "chooses which iTunes library file is written"},
	"itunes.libraries.import_source": {FieldServerPath, "chooses which iTunes library file is read"},
	"plugins[].settings":             {FieldServerPath, "free-form plugin settings, which can name paths and programs"},

	// ---- looked at and left unprotected (names matched the test's pattern) ----
	"plugins[].enabled":                    {FieldUnprotected, "toggles code already in the binary"},
	"exclude_patterns":                     {FieldUnprotected, "narrows what a scan reads; opens nothing new"},
	"supported_extensions":                 {FieldUnprotected, "narrows what a scan reads; opens nothing new"},
	"dedup_boilerplate":                    {FieldUnprotected, "title-matching regexes"},
	"fingerprint_worker_tool_versions":     {FieldUnprotected, "version strings compared with what workers report"},
	"fingerprint_window_reference_tools":   {FieldUnprotected, "version string"},
	"filename_parse_model":                 {FieldUnprotected, "a model name"},
	"purge_soft_deleted_delete_files":      {FieldUnprotected, "a switch over files the library already owns; names no path"},
	"itunes.path_trim_enabled":             {FieldUnprotected, "a switch; names no path"},
	"tools.allow_periodic_ollama":          {FieldUnprotected, "schedules the already-configured binary"},
	"tools.ollama_debounce_min":            {FieldUnprotected, "a delay"},
	"ai_endpoints_routing":                 {FieldUnprotected, "a switch"},
	"ai_backend.embedding_mode":            {FieldUnprotected, "local vs remote model, not a program path"},
	"ai_backend.llm_mode":                  {FieldUnprotected, "local vs remote model, not a program path"},
	"api_rate_limit_per_minute":            {FieldUnprotected, "general API throughput; sign-in has its own limit"},
	"metadata_sources[].rate_limit":        {FieldUnprotected, "outbound request pacing"},
	"metadata_sources[].requires_auth":     {FieldUnprotected, "says whether a source needs credentials; changes no sign-in"},
	"abs_auth_probe_enabled":               {FieldUnprotected, "a diagnostic probe; changes no sign-in"},
	"abs_server_version":                   {FieldUnprotected, "a version string reported to ABS clients"},
	"abs_default_library_id":               {FieldUnprotected, "which library ABS clients see first"},
	"abs_itunes_position_backfill_user_id": {FieldUnprotected, "whose listening positions a backfill writes; no credential"},
	// Third-party credentials: the server sends them out; GET masks them, so
	// replacing one cannot reveal it.
	"openai_api_key":                 {FieldUnprotected, "third-party credential"},
	"acoustid_api_key":               {FieldUnprotected, "third-party credential"},
	"google_books_api_key":           {FieldUnprotected, "third-party credential"},
	"hardcover_api_token":            {FieldUnprotected, "third-party credential"},
	"deluge_web_password":            {FieldUnprotected, "third-party credential"},
	"metadata_sources[].credentials": {FieldUnprotected, "third-party credentials"},
	// Outbound endpoints. They name hosts the server CALLS, not paths it
	// opens or programs it runs, so they are outside the interactive-session
	// rule. Some calls carry a stored credential (the OpenAI key, a download
	// client password), so pointing one elsewhere can expose that credential;
	// that is filed for the owner to decide (todo.d), not silently included.
	"openai_base_url":             {FieldUnprotected, "outbound endpoint"},
	"embedding.base_url":          {FieldUnprotected, "outbound endpoint"},
	"ai_backend.local_base_url":   {FieldUnprotected, "outbound endpoint"},
	"ai_endpoints":                {FieldUnprotected, "outbound endpoints (host_roots map remote hosts' paths, not the server's)"},
	"whisper_remote_url":          {FieldUnprotected, "outbound endpoint"},
	"whisper_endpoints":           {FieldUnprotected, "outbound endpoints"},
	"metadata_sources[].base_url": {FieldUnprotected, "outbound endpoint"},
	"otel_exporter_otlp_endpoint": {FieldUnprotected, "outbound endpoint"},
	"deluge_web_url":              {FieldUnprotected, "outbound endpoint"},
	"download_client":             {FieldUnprotected, "outbound endpoints"},
}

// ConfigFieldClass returns the class of the field at a JSON path (as the
// rules spell it), using the nearest classified ancestor.
func ConfigFieldClass(path string) FieldClass {
	for p := path; p != ""; p = parentFieldPath(p) {
		if r, ok := configFieldRules[p]; ok {
			return r.Class
		}
	}
	return FieldUnprotected
}

func parentFieldPath(p string) string {
	if strings.HasSuffix(p, "[]") {
		return strings.TrimSuffix(p, "[]")
	}
	i := strings.LastIndex(p, ".")
	if i < 0 {
		return ""
	}
	return p[:i]
}

// ChangedProtectedFields returns, sorted, the protected fields whose value
// differs between before and after. It compares the decoded structs, not the
// request body, so it sees exactly what the decoder applied — whatever the
// spelling or case of the keys that carried it (the 2026-10-07 review found
// that a key-name check could be walked around with "OAuth_Default_Role",
// which encoding/json still decodes onto oauth_default_role).
func ChangedProtectedFields(before, after *Config) []string {
	if before == nil || after == nil {
		return nil
	}
	var changed []string
	for path, rule := range configFieldRules {
		if !rule.Class.Protected() {
			continue
		}
		a, errA := projectField(reflect.ValueOf(*before), strings.Split(path, "."))
		b, errB := projectField(reflect.ValueOf(*after), strings.Split(path, "."))
		if errA != nil || errB != nil || !reflect.DeepEqual(a, b) {
			// A rule that no longer resolves is reported as changed: the
			// check fails closed, and the completeness test names it.
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}

// ownerTrustRootFields are the settings that decide WHO the owner is or how
// the owner's identity is proven (auth.OwnerProofWhyNot). They are sign-in
// settings too (configFieldRules), so an API key can never change them; on
// top of that, once owner_email is set, only the owner may change them
// (UpdateService; plan D15), whatever the caller's auth method and whether or
// not local auth is on. Without this a second admin signed in through
// Access, or a stolen password session, could set owner_email to itself, or
// point cf_access_* at a team it controls and mint the owner's JWT.
//
// Not here, with the reason: oauth_default_role and the rest of the sign-in
// class decide roles or other sign-in methods, none of which is the owner
// proof; basic auth, ABS and rate limits likewise.
var ownerTrustRootFields = map[string]string{
	"owner_email":           "names the owner",
	"cf_access_team_domain": "which Cloudflare Access team mints the JWT that proves the owner",
	"cf_access_aud":         "which Cloudflare Access application's JWT proves the owner",
	"enable_auth":           "turns every other sign-in guard off",
	"oauth_allowed_emails":  "which Access identities are admitted at all",
}

// ChangedOwnerTrustRoot returns the ownerTrustRootFields that differ between
// before and after, sorted. Same comparison as ChangedProtectedFields.
func ChangedOwnerTrustRoot(before, after *Config) []string {
	if before == nil || after == nil {
		return nil
	}
	var changed []string
	for path := range ownerTrustRootFields {
		a, errA := projectField(reflect.ValueOf(*before), strings.Split(path, "."))
		b, errB := projectField(reflect.ValueOf(*after), strings.Split(path, "."))
		if errA != nil || errB != nil || !reflect.DeepEqual(a, b) {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}

// projectField returns the value at segs below v. A segment ending in "[]"
// projects every element of a slice or map (maps keyed, so a renamed key is a
// change).
func projectField(v reflect.Value, segs []string) (any, error) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
	}
	if len(segs) == 0 {
		if !v.IsValid() {
			return nil, nil
		}
		return v.Interface(), nil
	}
	seg := segs[0]
	each := strings.HasSuffix(seg, "[]")
	name := strings.TrimSuffix(seg, "[]")
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("config field rule: %q is below a non-struct", seg)
	}
	f, ok := structFieldByJSONName(v, name)
	if !ok {
		return nil, fmt.Errorf("config field rule: no field %q", name)
	}
	if !each {
		return projectField(f, segs[1:])
	}
	switch f.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, f.Len())
		for i := range f.Len() {
			x, err := projectField(f.Index(i), segs[1:])
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil
	case reflect.Map:
		out := make(map[string]any, f.Len())
		iter := f.MapRange()
		for iter.Next() {
			x, err := projectField(iter.Value(), segs[1:])
			if err != nil {
				return nil, err
			}
			out[fmt.Sprint(iter.Key().Interface())] = x
		}
		return out, nil
	default:
		return nil, fmt.Errorf("config field rule: %q is not a slice or map", seg)
	}
}

func structFieldByJSONName(v reflect.Value, name string) (reflect.Value, bool) {
	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if tag == "" {
			tag = sf.Name
		}
		if tag == name {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

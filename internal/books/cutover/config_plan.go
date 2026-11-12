// file: internal/books/cutover/config_plan.go
// version: 1.4.0
// guid: 0f2f4b87-3f6d-4a4c-8e0a-7c7f2c64b0d1
// last-edited: 2026-11-12

package cutover

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// M2a's planning half (05-config §1.12.2): from what HEAD stored (the blob,
// the four encrypted rows, config.yaml's gap-fill values) to the list of
// writes M2 makes, with no I/O. config_step.go reads the inputs and applies
// the plan inside one chunk's batch. Nothing here carries a setting or
// secret value into an error, a reason or a log line: they name rows, keys,
// kinds and error classes only.

// Quarantine reasons: fixed text, never a value (the coordinator's note 1 and
// 05-storage §5S.3.13 "Reason must never carry a value").
const (
	reasonUnmapped       = "unmapped blob field: no rename-table row names it"
	reasonRefusedUnknown = "refused by config: the key is not registered"
	reasonRefusedStore   = "refused by config: the key cannot be stored"
	reasonRefusedValue   = "refused by config: the value is not valid for the key"
	reasonNotConvertible = "refused: the value does not convert to the canonical key's type"
	reasonUnreadableRow  = "unreadable settings row: the ciphertext does not decrypt"
	reasonSecretConflict = "refused by secrets: the name already holds other content"
	reasonSecretName     = "refused by secrets: the derived name is not a valid secret name"
	reasonOAuthDisabled  = "oauth sign-in is disabled: no provider entry was created from this client id"
	reasonOperatorSecret = "an operator-layer-only secret was found in the blob"
	reasonWebhookNoURL   = "the webhook secret has no URL to attach to"
	reasonPluginName     = "the plugin id is not a valid secret name segment"

	reasonURLUnproven     = "the URL may carry a credential that could not be split out: nothing was staged"
	reasonOAuthNoProvider = "oauth settings had no provider entry to attach to: no client id was set"
)

// The unreadable markers' reasons (secrets.Importer.ImportUnreadable): no key
// to try, or bytes the key does not open.
const (
	markerKey     = "key"
	markerCorrupt = "corrupt"
)

// settingsKeyPrefix is the key prefix of HEAD's settings rows: setting:<name>.
const settingsKeyPrefix = "setting:"

// blobRowName is the settings blob's row name.
const blobRowName = "config_blob"

// settingKey is the absolute key of settings row name.
func settingKey(name string) []byte { return []byte(settingsKeyPrefix + name) }

// blobKey is the settings blob's key.
func blobKey() []byte { return settingKey(blobRowName) }

// quarantineKey is the synthetic key of one member of the blob (a field no
// row names, a value config refused, a secret with nowhere to go): the blob's
// key and the member's dotted name, so it falls under the blob's Sensitive
// prefix and is sealed, and the record names what it holds.
func quarantineKey(member string) []byte {
	return []byte(settingsKeyPrefix + blobRowName + "#" + member)
}

// rowState is one encrypted row's outcome.
type rowState struct {
	Key      []byte // the row's key
	Raw      []byte // the stored bytes; nil when the row is absent
	Present  bool
	Value    string // the decrypted value when Readable
	Readable bool
	// Reason is the fixed class of an unreadable row, "" when Readable.
	Reason string
	// Marker is the unreadable marker's reason in secrets' vocabulary: "key"
	// when no key was available to try, "corrupt" when the stored bytes do not
	// decode or decrypt with the key there is. "" when Readable.
	Marker string
}

// m2Input is everything M2a reads.
type m2Input struct {
	Rows     map[string]*rowState // the four rows by name
	Blob     []byte               // the blob row's stored bytes, nil when absent
	Markers  headMarkers
	MaintSet bool // the maintenance_window_migrated marker row is "true"
	File     headFileValues
	FileHas  map[string]bool
	// FileSettings are config.yaml's viper-read settings (headViperFileKeys),
	// by lower-case name, each cast to the type configFromViper reads the key as
	// (headCastFileValue): a float64 for the int key, a bool or a string.
	FileSettings map[string]any
	// FileUnrepresentable names the viper-read keys whose file value cannot be
	// carried exactly (an int that is not finite or beyond 2^53); they are not in
	// FileSettings, and a key the blob lacks is refused, never read as absent.
	FileUnrepresentable map[string]bool
	Key                 []byte // the settings key, nil when none was found
}

// secretWrite is one secret M2 imports.
type secretWrite struct {
	Name  secrets.Name
	Value string
	// Source is the legacy key the value came from: the setting row that held
	// it, else the blob member (config.yaml's gap-fill of a row reads as the
	// member too). It is the key the value is quarantined under when its name
	// already holds other content.
	Source []byte
	// FromFile marks a value that only config.yaml held (HEAD's gap-fill: the
	// file applies where the stored value is empty). Such a value is not a
	// source of truth a replan can reconstruct: once the first run commits and
	// the row is gone, the file's value looks like a fresh candidate beside
	// the value the row had given. A name that already holds content therefore
	// means the value was applied (by this step's own earlier commit; core's
	// secrets are dark before the cutover), and the file's value is skipped,
	// never quarantined.
	FromFile bool
}

// stagedKey is one key M2 stages.
type stagedKey struct {
	Head     string
	Path     config.Path
	Value    json.RawMessage
	Original json.RawMessage // the value as stored, quarantined if config refuses Value
	Member   string          // the blob member, the quarantine key's suffix
}

// quarantined is one row or blob member M2 quarantines (sealed: both live
// under Sensitive prefixes).
type quarantined struct {
	Key    []byte
	Raw    []byte
	Reason string
}

// unreadableSecret is a row whose secret could not be read and for which no
// other source has a value: its name is marked unreadable, the row's bytes
// quarantined.
type unreadableSecret struct {
	Name   secrets.Name
	Row    string
	Reason string
}

// plan is M2a's writes.
type plan struct {
	Stages      []stagedKey
	Secrets     []secretWrite
	Unreadable  []unreadableSecret
	Quarantines []quarantined
	DeleteRows  [][]byte // rows removed after their value moved
	BlobKey     []byte
	BlobExpect  []byte // the blob row as read
	BlobNew     []byte // the blob row to write; nil when there was no blob
	Dropped     []string
	Reported    []string // notes for the report: names, never values
	Counts      map[string]int
}

func (p *plan) count(outcome string) { p.Counts[outcome]++ }

// empty reports whether the plan writes nothing: M2 has already run.
func (p *plan) empty() bool {
	return len(p.Stages) == 0 && len(p.Secrets) == 0 && len(p.Unreadable) == 0 && len(p.Quarantines) == 0 &&
		len(p.DeleteRows) == 0 && p.BlobNew == nil
}

// ---- settings rows ----

// decodeSettingRow decodes a stored settings row.
func decodeSettingRow(raw []byte) (headSetting, error) {
	var s headSetting
	if err := json.Unmarshal(raw, &s); err != nil {
		return headSetting{}, errors.New("the row is not a settings row")
	}
	return s, nil
}

// readRow decodes row name's outcome with key (nil: no key). A row with no
// encrypted value is Readable with an empty value; one whose ciphertext does
// not decrypt, or that is not a settings row, is not Readable.
func readRow(name string, raw []byte, key []byte) *rowState {
	r := &rowState{Key: settingKey(name), Raw: raw, Present: raw != nil}
	if raw == nil {
		return r
	}
	s, err := decodeSettingRow(raw)
	if err != nil {
		r.Reason, r.Marker = reasonUnreadableRow, markerCorrupt
		return r
	}
	if !s.IsSecret || s.Value == "" {
		r.Value, r.Readable = s.Value, true
		return r
	}
	plain, err := headDecrypt(key, s.Value)
	if err != nil {
		r.Reason, r.Marker = reasonUnreadableRow, markerCorrupt
		if key == nil {
			r.Marker = markerKey
		}
		return r
	}
	r.Value, r.Readable = plain, true
	return r
}

// rowsNeedKey reports whether any of the four rows holds an encrypted value:
// those are the rows that become unrecoverable if the key is lost.
func rowsNeedKey(raws map[string][]byte) bool {
	var rows []headSetting
	for _, name := range headSecretRows() {
		if raw := raws[name]; raw != nil {
			if s, err := decodeSettingRow(raw); err == nil {
				rows = append(rows, s)
			} else {
				return true // an unparseable row may hold anything: it needs the key's verdict
			}
		}
	}
	return headHasEncryptedValue(rows)
}

// peekRootDir reads root_dir from a blob without the transforms, for the key
// search that must precede the decode.
func peekRootDir(blob []byte, fileRoot string) string {
	if s, err := decodeSettingRow(blob); err == nil {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(s.Value), &m) == nil {
			var v string
			if json.Unmarshal(m["root_dir"], &v) == nil && v != "" {
				return v
			}
		}
	}
	return fileRoot
}

// keyDirs are the directories searched for the key file, in order: the state
// directory, then the database's directory (HEAD's bootSettings passes exactly
// these two to FindEncryptionKey), then <root_dir>/.appdata/ as an extra
// fallback. An empty directory is skipped.
func keyDirs(stateDir, dbDir, rootDir string) []string {
	dirs := []string{stateDir}
	if dbDir != "" {
		dirs = append(dirs, dbDir)
	}
	if rootDir != "" {
		dirs = append(dirs, filepath.Join(rootDir, ".appdata"))
	}
	return dirs
}

// ---- the JSON tree ----

func decodeTree(fields map[string]json.RawMessage) (map[string]any, error) {
	tree := make(map[string]any, len(fields))
	for name, raw := range fields {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("blob member %s does not decode", name)
		}
		tree[name] = v
	}
	return tree, nil
}

func getAt(tree map[string]any, segs []string) (any, bool) {
	var cur any = tree
	for _, s := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[s]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func delAt(tree map[string]any, segs []string) {
	m := tree
	for i, s := range segs {
		if i == len(segs)-1 {
			delete(m, s)
			return
		}
		next, ok := m[s].(map[string]any)
		if !ok {
			return
		}
		m = next
	}
}

// pruneEmpty removes objects left empty below the top level (a top-level
// member stays, however empty, when keep names it).
func pruneEmpty(m map[string]any, depth int, keep func(name string) bool) {
	for name, v := range m {
		child, ok := v.(map[string]any)
		if !ok {
			continue
		}
		pruneEmpty(child, depth+1, keep)
		if len(child) == 0 && !(depth == 0 && keep(name)) {
			delete(m, name)
		}
	}
}

func marshalAt(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("cutover: marshal of a decoded value: " + err.Error())
	}
	return b
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// ---- the plan ----

// planner builds a plan from inputs.
type planner struct {
	in   *m2Input
	tab  *RenameTable
	tree map[string]any
	p    *plan
	// imported is the set of secret names the plan imports, for the OIDC fold.
	imported map[secrets.Name]bool
	keepTop  map[string]bool // top-level class-i members, kept even when emptied
	oidc     *oidcFold
}

// buildPlan plans M2a for in. A blob that does not decode, or HEAD's codec
// refuses, is an error: HEAD itself refuses to run on such a blob, and the
// operator fixes it on the pre-freeze release (the step's Preflight reports
// it).
func buildPlan(in *m2Input) (*plan, error) {
	pl := &planner{in: in, tab: Table(), p: &plan{Counts: map[string]int{}, BlobKey: blobKey()}, imported: map[secrets.Name]bool{}, keepTop: map[string]bool{}}
	for _, r := range pl.tab.Rows {
		if r.Top && r.Class == ClassInput {
			pl.keepTop[r.Head] = true
		}
	}
	// The effective OpenAI key seeds the AI-endpoints transform (HEAD reads it
	// from the decrypted row, or from the blob/file where there is none).
	var blobSetting headSetting
	if in.Blob != nil {
		s, err := decodeSettingRow(in.Blob)
		if err != nil {
			return nil, errors.New("the settings blob row is not a settings row")
		}
		blobSetting = s
	}
	rows := map[string]*rowState{}
	for _, name := range headSecretRows() {
		if rs := in.Rows[name]; rs != nil {
			rows[name] = rs
		} else {
			rows[name] = &rowState{Key: settingKey(name)}
		}
	}
	pl.p.BlobExpect = in.Blob
	if in.Blob != nil {
		hasOpenAI := false
		if rs := rows["openai_api_key"]; rs.Readable && rs.Value != "" {
			hasOpenAI = true
		} else if in.File.OpenAIAPIKey != "" {
			hasOpenAI = true
		}
		blob, err := headDecodeBlob([]byte(blobSetting.Value), headSeed{HasOpenAIKey: hasOpenAI}, in.Markers)
		if err != nil {
			return nil, errors.New("the settings blob does not decode")
		}
		tree, err := decodeTree(blob.Fields)
		if err != nil {
			return nil, err
		}
		pl.tree = tree
		headEffectiveTree(pl.tree, in.MaintSet)
		// A file value M2 cannot carry exactly is HEAD's effective setting when
		// the blob lacks the key, and the strip would delete it: refuse by name.
		if keys := unrepresentableLacking(in, pl.tree); len(keys) > 0 {
			return nil, &unrepresentableError{Keys: keys}
		}
		pl.gapFill()
	} else {
		pl.tree = map[string]any{}
		// What config.yaml holds that HEAD reads as a setting (root_dir and the
		// viper-read keys that differ from their defaults) is applied to the blob,
		// where M6 and the keys' stages take it from, so without a blob it has
		// nowhere to go.
		if keys := fileBlobBound(in); len(keys) > 0 {
			return nil, &noBlobError{Keys: keys}
		}
	}

	pl.secretRows(rows)
	pl.rowsPass()
	pl.metadataSources()
	pl.plugins()
	pl.fold()
	pl.unknown()

	if in.Blob != nil {
		pruneEmpty(pl.tree, 0, func(name string) bool { return pl.keepTop[name] })
		nb := marshalAt(pl.tree)
		row := blobSetting
		row.Value = string(nb)
		pl.p.BlobNew = marshalAt(row)
	}
	return pl.p, nil
}

// gapFill applies config.yaml's gap-fill values the way HEAD does: root_dir
// where the stored value is empty, enable_ai_parsing where the stored value
// is false and the file says true. (The three file secrets are applied with
// the secret rows.)
//
// It also mirrors the seeding LoadConfigFromDatabase gets from Snapshot():
// HEAD decodes the blob onto the live config, which at that point holds
// viper's view, config.yaml included, so a member the blob lacks keeps the
// file's value. For each key configFromViper reads from the file
// (headViperFileKeys) that the blob lacks (absent or null: json.Unmarshal
// leaves a field alone for either), the file's value, cast to the key's type,
// is planted in the tree; the key stage then stages it like a blob member and
// the strip can remove the line. A value equal to HEAD's default is planted
// too: the key stage stages a blob member at its default (keyMember has no
// default check) and the registry skips a default-equal value
// (Importer.Stage), so a file value is held to the same rule, and where core's
// default is not HEAD's (concurrent_scans follows the host) a pinned value
// survives as it does for a blob member. Without this the strip deleted a
// value nothing stored.
func (pl *planner) gapFill() {
	for _, k := range headViperFileKeys {
		v, ok := pl.in.FileSettings[k]
		if !ok || pl.tree[k] != nil {
			continue
		}
		pl.tree[k] = v
	}
	if pl.in.File.RootDir != "" {
		if cur, _ := asString(pl.tree["root_dir"]); cur == "" {
			pl.tree["root_dir"] = pl.in.File.RootDir
		}
	}
	if pl.in.File.EnableAIParsing {
		if b, ok := pl.tree["enable_ai_parsing"].(bool); ok && !b {
			pl.tree["enable_ai_parsing"] = true
		}
	}
}

// secretRows plans the four encrypted rows: HEAD's effective value is the
// blob member, overridden by a readable row, then gap-filled from config.yaml
// where still empty. A value moves under its canonical name and its row is
// deleted; a row that does not decrypt is quarantined (sealed) and, when no
// other source has a value, its name is marked unreadable.
func (pl *planner) secretRows(rows map[string]*rowState) {
	fileVals := map[string]string{
		"openai_api_key":       pl.in.File.OpenAIAPIKey,
		"google_books_api_key": pl.in.File.GoogleBooksAPIKey,
		"hardcover_api_token":  pl.in.File.HardcoverAPIToken,
	}
	for _, r := range pl.tab.Rows {
		if !r.Top || r.Class != ClassSecret || r.Stored != StoredRow {
			continue
		}
		name := secrets.Name(r.Targets[0].Path)
		rs := rows[r.Head]
		v, _ := asString(pl.tree[r.Head])
		delAt(pl.tree, []string{r.Head})
		if rs.Present && rs.Readable {
			v = rs.Value
		}
		fromFile := false
		if v == "" {
			v = fileVals[r.Head]
			fromFile = v != ""
		}
		source := quarantineKey(r.Head)
		if rs.Present && rs.Readable && rs.Value != "" {
			source = rs.Key
		}
		switch {
		case rs.Present && !rs.Readable:
			pl.p.Quarantines = append(pl.p.Quarantines, quarantined{Key: rs.Key, Raw: rs.Raw, Reason: rs.Reason})
			if v != "" {
				pl.p.Secrets = append(pl.p.Secrets, secretWrite{Name: name, Value: v, Source: source, FromFile: fromFile})
				pl.imported[name] = true
				if fromFile {
					// HEAD parity (LoadConfigFromDatabase "will try config file
					// fallback"): the row did not decrypt and config.yaml had the
					// value. Counted and logged apart from an ordinary gap-fill,
					// so an operator can see a secret came back from the file.
					pl.p.Reported = append(pl.p.Reported, "secret "+r.Head+" recovered from config.yaml after an undecryptable row")
					pl.p.count("recovered-from-config-yaml")
				}
			} else {
				pl.p.Unreadable = append(pl.p.Unreadable, unreadableSecret{Name: name, Row: r.Head, Reason: rs.Marker})
			}
		default:
			if v != "" {
				pl.p.Secrets = append(pl.p.Secrets, secretWrite{Name: name, Value: v, Source: source, FromFile: fromFile})
				pl.imported[name] = true
			}
			if rs.Present {
				pl.p.DeleteRows = append(pl.p.DeleteRows, rs.Key)
			}
		}
	}
}

// rowsPass walks the table in order and moves every consumed leaf of the
// blob: class k to its keys, class s to its secrets, class x and operator-
// layer members dropped by rule. Class i rows stay, and container rows have
// only their leaves.
func (pl *planner) rowsPass() {
	for i := range pl.tab.Rows {
		r := &pl.tab.Rows[i]
		// A struct-typed row has only its leaves, unless the table drops the
		// whole struct by rule (ai_backend).
		whole := r.Container && r.Class == ClassDropped
		if r.BlobField == "" || r.Stored != StoredBlob || r.Container && !whole || strings.ContainsAny(r.Head, "[]<>") || placeholderSecret(r) {
			continue
		}
		segs := strings.Split(r.Head, ".")
		val, ok := getAt(pl.tree, segs)
		if !ok {
			continue
		}
		switch r.Class {
		case ClassInput:
			continue // stays in the blob for M6 (its secrets are separate rows)
		case ClassDropped:
			delAt(pl.tree, segs)
			if !headIsDefault(r.Head, val) {
				pl.p.Dropped = append(pl.p.Dropped, r.Head)
			}
			pl.p.count("dropped-by-rule")
		case ClassEnv:
			delAt(pl.tree, segs)
			pl.p.Dropped = append(pl.p.Dropped, r.Head)
			pl.p.count("dropped-by-rule")
		case ClassSecret:
			delAt(pl.tree, segs)
			pl.secretMember(r, val)
		case ClassKey:
			delAt(pl.tree, segs)
			pl.keyMember(r, val)
		}
	}
	// Operator-layer-only members (json "-" fields) and secrets named by
	// env/file-stored rows cannot be decoded from a blob; one found there is
	// dropped by rule, or, for a secret, quarantined sealed rather than
	// dropped in silence.
	for i := range pl.tab.Rows {
		r := &pl.tab.Rows[i]
		if r.Stored != StoredEnvFile || !r.Top {
			continue
		}
		val, ok := pl.tree[r.Head]
		if !ok {
			continue
		}
		delete(pl.tree, r.Head)
		if r.Class == ClassSecret {
			if s, _ := asString(val); s != "" {
				pl.p.Quarantines = append(pl.p.Quarantines, quarantined{Key: quarantineKey(r.Head), Raw: marshalAt(val), Reason: reasonOperatorSecret})
			}
			continue
		}
		pl.p.Dropped = append(pl.p.Dropped, r.Head)
		pl.p.count("dropped-by-rule")
	}
}

// placeholderSecret reports whether a row's secret target names an instance
// the planner finds in the blob itself (plugins.webhook.settings.secret
// goes under one name per URL), so its own pass moves it, not rowsPass.
func placeholderSecret(r *Row) bool {
	for _, t := range r.Targets {
		if t.Kind == TargetSecret && !isConcretePath(t.Path) {
			return true
		}
	}
	return false
}

// secretMember moves a blob member of class s (plaintext) under its
// canonical name. An empty value leaves the secret unset.
func (pl *planner) secretMember(r *Row, val any) {
	s, ok := asString(val)
	if !ok || s == "" {
		return
	}
	for _, t := range r.Targets {
		if t.Kind != TargetSecret {
			continue
		}
		pl.addSecret(secrets.Name(t.Path), s, r.Head)
	}
}

// addSecret plans a secret, or quarantines it when its name is not valid.
func (pl *planner) addSecret(name secrets.Name, value, member string) {
	if err := name.Validate(); err != nil {
		pl.p.Quarantines = append(pl.p.Quarantines, quarantined{Key: quarantineKey(member), Raw: []byte(value), Reason: reasonSecretName})
		return
	}
	pl.p.Secrets = append(pl.p.Secrets, secretWrite{Name: name, Value: value, Source: quarantineKey(member)})
	pl.imported[name] = true
}

// keyMember converts a blob member of class k and plans its key writes, one
// per key target (the fold targets are the fold's).
func (pl *planner) keyMember(r *Row, val any) {
	raw := marshalAt(val)
	if val == nil {
		return
	}
	for _, t := range r.Targets {
		switch t.Kind {
		case TargetFold:
			pl.foldValue(r.Head, val)
		case TargetKey:
			if strings.HasSuffix(t.Path, ".*") || !isConcretePath(t.Path) {
				continue
			}
			out, dropped, err := Convert(t.Convert, raw)
			switch {
			case errors.Is(err, ErrNotStaged):
				pl.p.Dropped = append(pl.p.Dropped, r.Head)
				pl.p.count("dropped-by-rule")
				continue
			case err != nil:
				pl.p.Quarantines = append(pl.p.Quarantines, quarantined{Key: quarantineKey(r.Head), Raw: raw, Reason: reasonNotConvertible})
				continue
			}
			for _, d := range dropped {
				pl.p.Dropped = append(pl.p.Dropped, r.Head+"."+d)
			}
			pl.stageKey(r, t, out, raw)
		}
	}
}

// stageKey plans one key. A value that carries a URL has its userinfo split
// out first (guardURLs); one that may carry a credential it cannot split out
// is quarantined sealed and nothing is staged.
func (pl *planner) stageKey(r *Row, t Target, out, original json.RawMessage) {
	out, ok := pl.guardURLs(r, t, out, original)
	if !ok {
		return
	}
	pl.p.Stages = append(pl.p.Stages, stagedKey{Head: r.Head, Path: config.Path(t.Path), Value: out, Original: original, Member: r.Head})
}

// isURLName reports whether a leaf or field name holds a URL: url or endpoint,
// or a name ending in _url or _endpoint (base_url, remote_url,
// otlp_endpoint). The generator sets Row.URL from it, and guardURLs applies
// it to the fields of a list or object value, so a URL inside an element is
// found by the same rule as a URL key.
func isURLName(name string) bool {
	return name == "url" || name == "endpoint" || strings.HasSuffix(name, "_url") || strings.HasSuffix(name, "_endpoint")
}

// urlVerdict is what guarding one URL string found.
type urlVerdict int

const (
	urlClean    urlVerdict = iota // no userinfo
	urlSplit                      // userinfo found and removed
	urlUnproven                   // a credential cannot be ruled out
)

// splitURLUserinfo removes the userinfo of s. Userinfo is delimited by an @, so
// a string with none has none, whatever it parses as (a bare host:port is no
// URL to net/url, and it is the usual form of an OTLP endpoint). With an @ it
// returns urlUnproven when it cannot prove the userinfo is the one net/url
// reports: the string does not parse, or it parses with no userinfo but an @
// stands in its authority (a schemeless user:pw@host:port parses as an opaque
// URL, so net/url reports no User).
func splitURLUserinfo(s string) (clean, info string, v urlVerdict) {
	if !strings.Contains(s, "@") {
		return s, "", urlClean
	}
	u, err := url.Parse(s)
	if err != nil {
		return s, "", urlUnproven
	}
	if u.User != nil {
		info = u.User.String()
		u.User = nil
		return u.String(), info, urlSplit
	}
	if authorityHasAt(s) {
		return s, "", urlUnproven
	}
	return s, "", urlClean
}

// authorityHasAt reports an @ in the part of s that would be its authority:
// after the "://" of a scheme or the leading "//" of a network-path reference,
// or from the start of a string with neither, up to the first of / ? #. An @
// in a path or query is not userinfo.
func authorityHasAt(s string) bool {
	rest := s
	if i := strings.Index(s, "://"); i > 0 && !strings.ContainsAny(s[:i], "/?#@") {
		rest = s[i+3:]
	} else if strings.HasPrefix(s, "//") {
		rest = s[2:]
	}
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		rest = rest[:end]
	}
	return strings.Contains(rest, "@")
}

// userinfoAt is one userinfo found in a staged value: the element path (empty
// for a scalar key) and the userinfo.
type userinfoAt struct {
	segs []string
	info string
}

// guardURLs walks a value about to be staged and splits the userinfo out of
// every URL-typed string leaf: the key's own value when the row is a URL key,
// and the fields of list and object elements by name (isURLName). Each
// userinfo goes to a secret named by position (<key>.userinfo for a scalar,
// <key>.item-<n>.<field>.userinfo inside a list); the staged value keeps the
// URL without it. A leaf that may carry a credential it cannot split out
// quarantines the whole member sealed, and nothing is staged: false.
func (pl *planner) guardURLs(r *Row, t Target, out, original json.RawMessage) (json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return out, true // not a JSON value: config's validation refuses it
	}
	var (
		found    []userinfoAt
		unproven bool
	)
	var walk func(v any, segs []string, urlTyped bool) any
	walk = func(v any, segs []string, urlTyped bool) any {
		switch x := v.(type) {
		case string:
			if !urlTyped {
				return x
			}
			clean, info, verdict := splitURLUserinfo(x)
			switch verdict {
			case urlUnproven:
				unproven = true
			case urlSplit:
				found = append(found, userinfoAt{segs: slices.Clone(segs), info: info})
				return clean
			}
			return x
		case map[string]any:
			names := make([]string, 0, len(x))
			for k := range x {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, k := range names {
				x[k] = walk(x[k], append(slices.Clone(segs), k), isURLName(k))
			}
			return x
		case []any:
			for i := range x {
				x[i] = walk(x[i], append(slices.Clone(segs), "item-"+strconv.Itoa(i)), urlTyped)
			}
			return x
		}
		return v
	}
	v = walk(v, nil, r.URL)
	if unproven {
		pl.p.Quarantines = append(pl.p.Quarantines, quarantined{Key: quarantineKey(r.Head), Raw: original, Reason: reasonURLUnproven})
		return nil, false
	}
	if len(found) == 0 {
		return out, true
	}
	for _, f := range found {
		name, member := t.Path, r.Head
		if len(f.segs) > 0 {
			name += "." + strings.Join(f.segs, ".")
			member += "." + strings.Join(f.segs, ".")
		}
		pl.addSecret(secrets.Name(name+".userinfo"), f.info, member)
		pl.p.Reported = append(pl.p.Reported, "userinfo moved out of "+name)
		pl.p.count("userinfo-record")
	}
	return marshalAt(v), true
}

// ---- the OIDC fold ----

// oidcFold is the oauth_* rows' values.
type oidcFold struct {
	seen    bool
	enabled bool
	ids     map[string]string
	emails  string
	role    string
}

// foldValue records one fold row's value; fold() builds the entries.
func (pl *planner) foldValue(head string, val any) {
	f := pl.foldState()
	f.seen = true
	s, _ := asString(val)
	switch head {
	case "oauth_enabled":
		f.enabled, _ = val.(bool)
	case "oauth_github_client_id":
		f.ids["github"] = s
	case "oauth_google_client_id":
		f.ids["google"] = s
	case "oauth_allowed_emails":
		f.emails = s
	case "oauth_default_role":
		f.role = s
	}
}

func (pl *planner) foldState() *oidcFold {
	if pl.oidc == nil {
		pl.oidc = &oidcFold{ids: map[string]string{}}
	}
	return pl.oidc
}

// oidcProviders are the two providers HEAD's sign-in knows, with the secret
// each one's client_secret reference names (05-config §1.12.3).
var oidcProviders = []struct {
	id, kind, issuer, secret string
}{
	{"github", "github", "", "core.auth.oidc.github.client_secret"},
	{"google", "oidc", "https://accounts.google.com", "core.auth.oidc.google.client_secret"},
}

// fold builds core.auth.oidc.providers from the oauth_* rows (05-authn): one
// entry per provider with a client id, when sign-in is enabled. With sign-in
// disabled no entry is created (an entry would turn SSO on) and the client
// ids are quarantined, sealed, instead of dropped.
func (pl *planner) fold() {
	f := pl.oidc
	if f == nil || !f.seen {
		return
	}
	var emails []string
	for _, e := range strings.Split(f.emails, ",") {
		if e = strings.TrimSpace(e); e != "" {
			emails = append(emails, e)
		}
	}
	var entries []any
	for _, p := range oidcProviders {
		id := f.ids[p.id]
		if id == "" {
			continue
		}
		if !f.enabled {
			// The client secret still moved to its name, which nothing references
			// while sign-in is off: the record pairs the id with that name, so the
			// operator can see they belong together.
			pair := map[string]any{"client_id": id}
			if pl.imported[secrets.Name(p.secret)] {
				pair["client_secret_name"] = p.secret
			}
			pl.p.Quarantines = append(pl.p.Quarantines, quarantined{Key: quarantineKey("oauth_" + p.id + "_client_id"), Raw: marshalAt(pair), Reason: reasonOAuthDisabled})
			continue
		}
		entry := map[string]any{"id": p.id, "kind": p.kind, "client_id": id}
		if p.issuer != "" {
			entry["issuer"] = p.issuer
		}
		if len(emails) > 0 {
			entry["allowed_emails"] = emails
		}
		if f.role != "" {
			entry["default_role"] = f.role
		}
		if pl.imported[secrets.Name(p.secret)] {
			entry["client_secret"] = map[string]any{"secret": p.secret}
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		// The default role and the allowed emails ride on the provider entries
		// (05-authn); with none to carry them they would vanish, so each is
		// quarantined sealed.
		reason := reasonOAuthNoProvider
		if !f.enabled {
			reason = reasonOAuthDisabled
		}
		// HEAD's blob always carries the role, "viewer" unless set: a default is
		// no information, so it is not quarantined on every install.
		if f.role != "" && !headIsDefault("oauth_default_role", f.role) {
			pl.p.Quarantines = append(pl.p.Quarantines, quarantined{Key: quarantineKey("oauth_default_role"), Raw: marshalAt(f.role), Reason: reason})
		}
		if f.emails != "" {
			pl.p.Quarantines = append(pl.p.Quarantines, quarantined{Key: quarantineKey("oauth_allowed_emails"), Raw: marshalAt(f.emails), Reason: reason})
		}
		return
	}
	raw := marshalAt(entries)
	pl.p.Stages = append(pl.p.Stages, stagedKey{Head: "oauth_enabled", Path: "core.auth.oidc.providers", Value: raw, Original: raw, Member: "oauth_enabled"})
}

// ---- metadata sources, plugins ----

// metadataSources moves each source's credentials out of the blob into
// integrations.metadata.<source id>.credentials.<key> secrets.
func (pl *planner) metadataSources() {
	list, ok := pl.tree["metadata_sources"].([]any)
	if !ok {
		return
	}
	for i, el := range list {
		src, ok := el.(map[string]any)
		if !ok {
			continue
		}
		creds, ok := src["credentials"].(map[string]any)
		if !ok {
			continue
		}
		id, _ := asString(src["id"])
		keys := make([]string, 0, len(creds))
		for k := range creds {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v, _ := asString(creds[k])
			if v == "" {
				continue
			}
			pl.addSecret(secrets.Name("integrations.metadata."+id+".credentials."+snakeSegment(k)), v, fmt.Sprintf("metadata_sources.%d.credentials.%s", i, k))
		}
		delete(src, "credentials")
	}
}

// snakeSegment spells a HEAD credential key as a secret-name segment: HEAD's
// metadata sources key their credentials in camel case ("apiKey"), a secret
// name segment is [a-z][a-z0-9_-]*. A key that is still no segment after this
// is quarantined by addSecret, not moved.
func snakeSegment(k string) string {
	var b strings.Builder
	for i, r := range k {
		switch {
		case r >= 'A' && r <= 'Z':
			if i > 0 && k[i-1] != '_' && k[i-1] != '-' && !(k[i-1] >= 'A' && k[i-1] <= 'Z') {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// plugins moves the webhook plugin's secret to one record per URL and every
// other plugin's settings map to one encrypted record, reported.
func (pl *planner) plugins() {
	plugins, ok := pl.tree["plugins"].(map[string]any)
	if !ok {
		return
	}
	ids := make([]string, 0, len(plugins))
	for id := range plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		entry, _ := plugins[id].(map[string]any)
		settings, _ := entry["settings"].(map[string]any)
		if id == "webhook" {
			pl.webhook(settings)
			continue
		}
		if len(settings) > 0 {
			name := secrets.Name("modules.books.plugins." + id + ".settings")
			if err := name.Validate(); err != nil {
				pl.p.Quarantines = append(pl.p.Quarantines, quarantined{Key: quarantineKey("plugins." + id), Raw: marshalAt(entry), Reason: reasonPluginName})
			} else {
				pl.p.Secrets = append(pl.p.Secrets, secretWrite{Name: name, Value: string(marshalAt(settings)), Source: quarantineKey("plugins." + id)})
				pl.imported[name] = true
				pl.p.Reported = append(pl.p.Reported, "plugin "+id+" settings kept as one encrypted record")
				pl.p.count("plugin-settings-record")
			}
		}
		delete(plugins, id)
	}
}

// webhook moves the webhook plugin's shared secret to
// integrations.notify.webhook-<n>.secret, one record per non-empty URL of
// settings.urls, n counting the kept entries from 1.
func (pl *planner) webhook(settings map[string]any) {
	secret, _ := asString(settings["secret"])
	delete(settings, "secret")
	if secret == "" {
		return
	}
	urls, _ := asString(settings["urls"])
	n := 0
	for _, u := range strings.Split(urls, ",") {
		if strings.TrimSpace(u) == "" {
			continue
		}
		n++
		pl.addSecret(secrets.Name(fmt.Sprintf("integrations.notify.webhook-%d.secret", n)), secret, "plugins.webhook.settings.secret")
	}
	if n == 0 {
		pl.p.Quarantines = append(pl.p.Quarantines, quarantined{Key: quarantineKey("plugins.webhook.settings.secret"), Raw: []byte(secret), Reason: reasonWebhookNoURL})
	}
}

// ---- unknown members ----

// unknown quarantines, sealed, every blob member no row accounts for: a top-
// level field no row names, or a nested field below a container row that is
// not one of its leaves. A class-i member's whole subtree stays.
func (pl *planner) unknown() {
	prefix := map[string]bool{} // heads that are a proper prefix of a row head
	input := map[string]bool{}  // class-i heads: their subtrees stay
	for _, r := range pl.tab.Rows {
		if strings.ContainsAny(r.Head, "[]<>") {
			continue
		}
		if r.Class == ClassInput {
			input[r.Head] = true
		}
		segs := strings.Split(r.Head, ".")
		for i := 1; i < len(segs); i++ {
			prefix[strings.Join(segs[:i], ".")] = true
		}
	}
	var walk func(m map[string]any, path string)
	walk = func(m map[string]any, path string) {
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			head := n
			if path != "" {
				head = path + "." + n
			}
			if input[head] {
				continue
			}
			if child, ok := m[n].(map[string]any); ok && prefix[head] {
				walk(child, head)
				continue
			}
			// A leaf that is a row's head was consumed already; anything left is unmapped.
			pl.p.Quarantines = append(pl.p.Quarantines, quarantined{Key: quarantineKey(head), Raw: marshalAt(m[n]), Reason: reasonUnmapped})
			delete(m, n)
		}
	}
	walk(pl.tree, "")
}

// Verification of a rewritten blob (Verify): the names of members that
// should have moved and did not.
func leftoverMembers(tree map[string]any) []string {
	pl := &planner{tab: Table(), tree: tree, p: &plan{Counts: map[string]int{}}}
	pl.unknown()
	var left []string
	for _, q := range pl.p.Quarantines {
		left = append(left, strings.TrimPrefix(string(q.Key), settingsKeyPrefix+blobRowName+"#"))
	}
	// A consumed leaf still present is also a leftover: every non-input row.
	for _, r := range pl.tab.Rows {
		if r.BlobField == "" || r.Stored != StoredBlob || r.Container || r.Class == ClassInput || strings.ContainsAny(r.Head, "[]<>") {
			continue
		}
		if _, ok := getAt(tree, strings.Split(r.Head, ".")); ok {
			left = append(left, r.Head)
		}
	}
	if list, ok := tree["metadata_sources"].([]any); ok {
		for _, el := range list {
			if m, ok := el.(map[string]any); ok {
				if _, has := m["credentials"]; has {
					left = append(left, "metadata_sources[].credentials")
					break
				}
			}
		}
	}
	if plugins, ok := tree["plugins"].(map[string]any); ok {
		for id, el := range plugins {
			if id != "webhook" {
				left = append(left, "plugins."+id)
				continue
			}
			if e, ok := el.(map[string]any); ok {
				if s, ok := e["settings"].(map[string]any); ok {
					if _, has := s["secret"]; has {
						left = append(left, "plugins.webhook.settings.secret")
					}
				}
			}
		}
	}
	slices.Sort(left)
	return slices.Compact(left)
}

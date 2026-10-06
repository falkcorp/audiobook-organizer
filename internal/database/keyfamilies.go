// file: internal/database/keyfamilies.go
// version: 1.5.0
// guid: 12fbfb04-5d87-4708-8975-48081212acb1
// last-edited: 2026-10-06

package database

import (
	"bytes"
	"sort"
	"strings"
)

// KeyFamily is one key prefix family in the main Pebble store.
//
// Owner is the file that writes the family. Description says what the keys
// hold. The registry below is the single source for the db-census endpoint and,
// later, for the generated key-schema document — add a family here when you add
// a new key prefix, or its keys will show up in the census as "(unregistered)".
type KeyFamily struct {
	Prefix      string `json:"prefix"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
}

// unregisteredFamily names every key range that no registered family covers.
const unregisteredFamily = "(unregistered)"

// keyFamilies is the registry. Nesting rule: a family whose prefix starts with
// another family's prefix is that family's child, and the parent's census
// figures exclude every registered child (see keyFamilyRanges).
//
// Several families are built from a runtime argument rather than a literal and
// so are invisible to a grep for their full prefix: the act:<tier>: primaries
// (pactPrimaryKey over actTiers) and book:asin:/book:isbn10:/book:isbn13:
// (isbnIndexKey). They are real on-disk families and are registered anyway.
var keyFamilies = []KeyFamily{
	// ── Activity log (PebbleActivityStore shares the main DB) ──
	{"act:", "activity log: tier primaries plus op/book/filter indexes", "internal/database/pebble_activity_store.go"},
	{"act:audit:", "activity primary rows, audit tier", "internal/database/pebble_activity_store.go"},
	{"act:batch:", "activity primary rows, batch tier", "internal/database/pebble_activity_store.go"},
	{"act:bk:", "activity index by book id", "internal/database/pebble_activity_store.go"},
	{"act:change:", "activity primary rows, change tier", "internal/database/pebble_activity_store.go"},
	{"act:debug:", "activity primary rows, debug tier", "internal/database/pebble_activity_store.go"},
	{"act:digest:", "activity primary rows, daily digest rollups", "internal/database/pebble_activity_store.go"},
	{"act:info:", "activity primary rows, info tier", "internal/database/pebble_activity_store.go"},
	{"act:lvl:", "activity filter index by level", "internal/database/pebble_activity_filter_index.go"},
	{"act:op:", "activity index by operation id", "internal/database/pebble_activity_store.go"},
	{"act:src:", "activity filter index by source", "internal/database/pebble_activity_filter_index.go"},
	{"act:system:", "activity primary rows, system tier", "internal/database/pebble_activity_store.go"},
	{"act:typ:", "activity filter index by type", "internal/database/pebble_activity_filter_index.go"},

	// ── AI ──
	{"aijob:", "AI job rows", "internal/database/pebble_store_aijobs.go"},
	{"aijob_batch:", "AI job index by batch id", "internal/database/pebble_store_aijobs.go"},
	{"aijob_payload:", "AI job request payloads", "internal/database/pebble_store_aijobs.go"},
	{"aijournal:", "durable AI result journal (whisper, batch)", "internal/ai/resultjournal/journal.go"},
	{"aiscan:", "AI scan history, phases, results and artifacts (shared-DB namespace)", "internal/database/ai_scan_store.go"},

	// ── Auth, users, sessions ──
	{"abs_sess:", "Audiobookshelf-compat sessions", "internal/database/pebble_store_abssession.go"},
	{"admin_debug_edit:", "admin debug-edit undo images", "internal/server/handlers/admindebug/handler.go"},
	{"apikey:", "API keys", "internal/database/pebble_store_auth.go"},
	{"idx:", "secondary indexes for auth, users, sessions, playback, playlists and collections", "internal/database/pebble_store_auth.go"},
	{"invite:", "user invites", "internal/database/pebble_store_auth.go"},
	{"oauthid:", "OAuth identities", "internal/database/oauth_identity.go"},
	{"role:", "roles", "internal/database/pebble_store_auth.go"},
	{"sess:", "web sessions", "internal/database/pebble_store_auth.go"},
	{"u:", "users", "internal/database/pebble_store_auth.go"},

	// ── Authors, narrators, series, works ──
	{"author:", "author rows", "internal/database/pebble_store_authors.go"},
	{"author:name:", "author index by normalized name", "internal/database/pebble_store_name_index.go"},
	{"author_alias:", "author aliases and their indexes", "internal/database/pebble_store_authors.go"},
	{"author_tag:", "author tags", "internal/database/pebble_store.go"},
	{"author_tag_idx:", "author tag reverse index", "internal/database/pebble_store.go"},
	{"author_tombstone:", "merged-away author id redirects", "internal/database/pebble_store_authors.go"},
	{"narrator:", "narrator rows", "internal/database/pebble_store_authors.go"},
	{"narrator_counter", "narrator id counter (single bare key)", "internal/database/pebble_store_authors.go"},
	{"narrator_name:", "narrator index by normalized name", "internal/database/pebble_store_name_index.go"},
	{"series:", "series rows", "internal/database/pebble_store_series.go"},
	{"series:name:", "series index by normalized name and author", "internal/database/pebble_store_name_index.go"},
	{"series_tag:", "series tags", "internal/database/pebble_store.go"},
	{"series_tag_idx:", "series tag reverse index", "internal/database/pebble_store.go"},
	{"work:", "work rows", "internal/database/pebble_store_works.go"},

	// ── Books ──
	{"alt_titles:", "alternate titles per book", "internal/database/pebble_store.go"},
	{"b:duration_map:", "per-book segment duration maps", "internal/database/pebble_store.go"},
	{"book:", "book rows (book:<ULID>) and the book:<index>: sub-families", "internal/database/pebble_store.go"},
	{"book:asin:", "book index by ASIN (multi-value)", "internal/database/pebble_store_isbn_index.go"},
	{"book:author:", "dead legacy index, swept by retention", "internal/maintenance/jobs/retention_and_hygiene.go"},
	{"book:hash:", "book index by file hash", "internal/database/pebble_store.go"},
	{"book:isbn10:", "book index by ISBN-10 (multi-value)", "internal/database/pebble_store_isbn_index.go"},
	{"book:isbn13:", "book index by ISBN-13 (multi-value)", "internal/database/pebble_store_isbn_index.go"},
	{"book:organizedhash:", "book index by organized file hash", "internal/database/pebble_store.go"},
	{"book:originalhash:", "book index by original file hash", "internal/database/pebble_store.go"},
	{"book:path:", "book index by file path", "internal/database/pebble_store.go"},
	{"book:series:", "dead legacy index, swept by retention", "internal/maintenance/jobs/retention_and_hygiene.go"},
	{"book:versiongroup:", "book index by version group", "internal/database/pebble_store.go"},
	{"book:work:", "book index by work", "internal/database/pebble_store.go"},
	{"book_atpath:", "books-at-path index", "internal/database/pebble_store_atpath_index.go"},
	{"book_atpath_undecodable:", "undecodable books-at-path markers", "internal/database/pebble_store_atpath_index.go"},
	{"book_authors:", "book to author links", "internal/database/pebble_store.go"},
	{"book_narrators:", "book to narrator links", "internal/database/pebble_store.go"},
	{"book_sig:", "book signatures", "internal/database/pebble_store_booksig.go"},
	{"book_tag:", "book tags", "internal/database/pebble_store.go"},
	{"book_ver:", "book version history snapshots (book_ver:<id>:<nanos>)", "internal/database/pebble_store.go"},
	{"blocked:hash:", "blocked file hashes", "internal/database/pebble_store_blocklist.go"},
	{"bv:", "book versions (torrent-backed)", "internal/database/pebble_store.go"},
	{"cover_text:", "cover OCR text records", "internal/covertext/covertext.go"},
	{"cover_text_book:", "cover OCR text index by book", "internal/covertext/covertext.go"},
	{"deferred_itunes:", "deferred iTunes updates", "internal/database/pebble_store_itunes.go"},
	{"ext_id:", "external id mappings", "internal/database/pebble_store_externalids.go"},
	{"import_path:", "import paths", "internal/database/pebble_store_importpaths.go"},
	{"itunes:", "iTunes library fingerprints", "internal/database/pebble_store_stats.go"},
	{"library:", "legacy library paths, migrated to import_path:", "internal/database/pebble_store.go"},
	{"merge_user_state_pending:", "pending user-state repairs after a merge", "internal/merge/pending_repair.go"},
	{"merge:combine-journal:", "book combine journal", "internal/merge/combine_journal.go"},
	{"merge:survivor-reconcile:", "per-user markers that make a touched survivor's put-back safe to re-run", "internal/merge/combine_journal.go"},
	{"scanner:ai_parse_single_fail:", "single-file AI parse give-up markers by path hash", "internal/scanner/ai_parse_giveup.go"},
	{"metadata_cache:", "metadata provider response cache", "internal/database/pebble_store_metadata_cache.go"},
	{"metadata_change:", "metadata change records", "internal/database/pebble_store_metadata.go"},
	{"metadata_fetch_cache:", "metadata fetch cache per book and source", "internal/database/metadata_fetch_cache.go"},
	{"metadata_rejection:", "rejected metadata candidates", "internal/database/pebble_store_metadata.go"},
	{"metadata_state:", "per-field metadata state", "internal/database/pebble_store_metadata.go"},
	{"path_history:", "book path change history", "internal/database/pebble_store_scancache.go"},
	{"rejected_candidate:", "rejected batch metadata candidates", "internal/server/metadata_batch_candidates.go"},
	{"revert_settle_owed:", "operation reverts owed a settle pass", "internal/audiobooks/revert_settle.go"},
	{"scan_fail:", "scan quarantine records by path hash", "internal/database/pebble_store_quarantine.go"},
	{"split_book_candidate:", "split-book dedup candidates", "internal/dedup/split_book_storage.go"},
	{"tombstone:", "deleted-book tombstones", "internal/database/pebble_store.go"},
	{"user_tag:", "user tags", "internal/database/pebble_store.go"},

	// ── Authority lists (internal/authority; rebuilt by maintenance.authority-build) ──
	{"ref_asin:", "authority lists: contributor ASIN to every name fold it was credited under", "internal/authority/authoritybuild/apply.go"},
	{"ref_ovr:", "authority lists: owner overrides for persons and publishers (never rebuilt)", "internal/authority/lookup.go"},
	{"ref_person:", "authority lists: known persons by name fold, with roles, tiers and contributor ASINs", "internal/authority/authoritybuild/apply.go"},
	{"ref_pub:", "authority lists: known publishers by name fold", "internal/authority/authoritybuild/apply.go"},
	{"ref_src:", "authority lists: per-source ingest ledger (item digest)", "internal/authority/authoritybuild/apply.go"},

	// ── Book files ──
	{"bf:", "book file segments", "internal/database/pebble_store.go"},
	{"bfs:", "book file segment index by book", "internal/database/pebble_store.go"},
	{"book_file:", "book file rows", "internal/database/pebble_store.go"},
	{"book_file_acoustid:", "book file index by AcoustID", "internal/database/pebble_store.go"},
	{"book_file_error:", "book file errors by path", "internal/database/pebble_book_file_errors.go"},
	{"book_file_errors_by_book:", "book file error index by book", "internal/database/pebble_book_file_errors.go"},
	{"book_file_gone:", "deleted book file markers", "internal/database/book_delete_owns_files.go"},
	{"book_file_hash:", "book file index by hash", "internal/database/pebble_store.go"},
	{"book_file_id:", "book file index by id", "internal/database/pebble_store.go"},
	{"book_file_orig_hash:", "book file index by original hash", "internal/database/pebble_store.go"},
	{"book_file_path:", "book file index by path CRC", "internal/database/pebble_store.go"},
	{"book_file_pid:", "book file index by iTunes persistent id", "internal/database/pebble_store.go"},
	{"chapters:", "per-book chapter lists", "internal/database/pebble_store_chapters.go"},
	{"file_prov:", "file provenance records", "internal/database/pebble_file_provenance.go"},
	{"file_prov_hash:", "file provenance index by hash", "internal/database/pebble_file_provenance.go"},
	{"file_prov_orphan:", "orphaned file provenance records", "internal/database/pebble_file_provenance.go"},
	{"file_prov_seq:", "file provenance sequence index", "internal/database/pebble_file_provenance.go"},
	{"pending_file_op:", "pending file operations for crash recovery", "internal/server/file_io_pool.go"},

	// ── Catalog ──
	{"cat:", "catalog entries", "internal/database/catalog_entry_store.go"},
	{"cat_author:", "catalog index by author", "internal/database/catalog_entry_store.go"},
	{"cat_author_state:", "catalog per-author harvest state", "internal/database/catalog_entry_store.go"},
	{"cat_eg:", "catalog entry groups", "internal/database/catalog_entry_store.go"},
	{"cat_egkey:", "catalog entry group keys", "internal/database/catalog_entry_store.go"},
	{"cat_hv:", "catalog harvest records", "internal/database/catalog_entry_store.go"},
	{"cat_id:", "catalog index by id", "internal/database/catalog_entry_store.go"},
	{"cat_pid:", "catalog index by provider id", "internal/database/catalog_entry_store.go"},
	{"cat_raw:", "catalog raw provider payloads", "internal/database/catalog_entry_store.go"},
	{"cat_series:", "catalog index by series", "internal/database/catalog_entry_store.go"},
	{"cat_stale:", "catalog staleness index", "internal/database/catalog_entry_store.go"},

	// ── Dedup and embeddings (EmbeddingStore shares the main DB) ──
	{"dedup:", "dedup engine state", "internal/database/embedding_store.go"},
	{"dedup:automerge:", "dedup auto-merge journal", "internal/database/dedup_automerge_journal.go"},
	{"dedup:e:", "dedup candidate index by entity", "internal/database/embedding_store.go"},
	{"dedup:label:", "dedup labels", "internal/database/dedup_label.go"},
	{"dedup:lbe:", "dedup label index by entity", "internal/database/dedup_label.go"},
	{"dedup:p:", "dedup candidate pair index", "internal/database/embedding_store.go"},
	{"dedup:r:", "dedup candidate records", "internal/database/embedding_store.go"},
	{"dedup:s:", "dedup candidate index by status", "internal/database/embedding_store.go"},
	{"dedup_candidate:", "legacy dedup candidates, read by quick queries", "internal/database/pebble_quick_queries.go"},
	{"emb:", "embeddings", "internal/database/embedding_store.go"},
	{"emb:c:", "embedding cache", "internal/database/embedding_store.go"},
	{"emb:v:", "embedding vectors", "internal/database/embedding_store.go"},

	// ── Fingerprints ──
	{"fpidx:", "fingerprint LSH index", "internal/database/pebble_store.go"},
	{"fpidx_meta:", "fingerprint LSH index metadata", "internal/database/pebble_store.go"},
	{"fpwin:", "windowed fingerprints", "internal/database/fingerprint_window.go"},
	{"fpwin_fail:", "windowed fingerprint failures", "internal/database/fingerprint_window.go"},

	// ── Metrics, counters, settings, system ──
	{"asinbackfill:", "ASIN/ISBN backfill per-book miss markers", "internal/plugins/metafetch/asin_backfill.go"},
	{"counter:", "id counters", "internal/database/pebble_store.go"},
	{"cursor:", "export cursors", "internal/database/pebble_file_provenance.go"},
	{"met:", "metrics store", "internal/database/pebble_metrics_store.go"},
	{"pref:", "per-user preferences", "internal/database/pebble_store_preferences.go"},
	{"pref:_system:", "the _system user's records (other than the sub-families below)", "internal/database/pebble_store_preferences.go"},
	{"pref:_system:apply_rename_failure:", "durable organizer apply-rename failure records", "internal/organizer/apply_failure.go"},
	{"pref:_system:itunes_clone_record:", "iTunes clone-into-library records", "internal/plugins/maintenance/itunes_clone_into_library.go"},
	{"pref:_system:organize_collision_skip:", "durable organize collision-skip records", "internal/organizer/apply_failure.go"},
	{"pref:_system:outbox:writeback:", "tag write-back outbox", "internal/writeback/outbox.go"},
	{"pref:_system:pipeline_checkpoint:", "organizer per-book phase checkpoints", "internal/organizer/checkpoint.go"},
	{"pref:_system:rename_path_write_failure:", "rename path-write failure records", "internal/organizer/rename_path_failure.go"},
	{"preference:", "global preferences", "internal/database/pebble_store_preferences.go"},
	{"provider_daily_budget:", "metadata provider daily lookup counts (shared Google Books budget)", "internal/metadata/dailyquota/dailyquota.go"},
	{"provider_throttle:", "metadata provider throttles", "internal/database/provider_throttle.go"},
	{"quick_query_cache:", "quick query result cache", "internal/database/pebble_quick_queries.go"},
	{"setting:", "settings", "internal/database/settings.go"},
	{"setting:repairs_last_apply_op:", "last repairs apply op per fixer", "internal/server/handlers/repairs/handler.go"},
	{"setting:repairs_last_plan_op:", "last repairs plan op per fixer", "internal/server/handlers/repairs/handler.go"},
	{"stats:", "cached library, transcribe and playback stats", "internal/database/pebble_store.go"},
	{"system:", "system flags and markers", "internal/database/pebble_store_atpath_index.go"},
	{"system:backfill:", "backfill completion markers and cursors", "internal/database/pebble_store_atpath_index.go"},
	{"system:census:", "last exact db census and its run progress", "internal/database/census_exact.go"},

	// ── Operations ──
	{"op:", "legacy op dedup and completion records", "internal/database/pebble_store_ops_v2.go"},
	{"op:batch:", "op batch membership", "internal/database/pebble_store_ops_v2.go"},
	{"op:completion:", "op completion records per subject", "internal/database/pebble_store_ops_v2.go"},
	{"op:deprev:", "op dependency revisions per subject", "internal/database/pebble_store_ops_v2.go"},
	{"op_result:", "per-book operation results", "internal/database/pebble_store_operations.go"},
	{"opchange:", "operation change journal", "internal/database/pebble_store_operations.go"},
	{"opchange_by_book:", "operation change index by book", "internal/database/pebble_store_opchange_index.go"},
	{"opchange_undecodable:", "undecodable operation change markers", "internal/database/pebble_store_opchange_index.go"},
	{"operation:", "v1 operation rows", "internal/database/pebble_store_operations.go"},
	{"operationlog:", "v1 operation logs", "internal/database/pebble_store_operations.go"},
	{"opstate:", "v1 operation state and params", "internal/database/pebble_store_operations.go"},
	{"opsummary:", "v1 operation summaries", "internal/database/pebble_store_operations.go"},
	{"opv2:", "operations v2", "internal/database/pebble_store_ops_v2.go"},
	{"opv2:act:", "operations v2 active set", "internal/database/pebble_store_ops_v2.go"},
	{"opv2:def:", "operations v2 definitions", "internal/database/pebble_store_ops_v2.go"},
	{"opv2:done:", "timeline index by completed_at nanos (TASK-A5)", "internal/database/pebble_store_ops_v2.go"},
	{"opv2:err:", "operations v2 errors", "internal/database/pebble_store_ops_v2.go"},
	{"opv2:log:", "operations v2 logs", "internal/database/pebble_store_ops_v2.go"},
	{"opv2:op:", "operations v2 rows", "internal/database/pebble_store_ops_v2.go"},
	{"opv2:open:", "timeline index: op not completed (TASK-A5)", "internal/database/pebble_store_ops_v2.go"},
	{"opv2:q:", "operations v2 queue", "internal/database/pebble_store_ops_v2.go"},
	{"opv2:state:", "operations v2 resume state", "internal/database/pebble_store_ops_v2.go"},
	{"opv2:strike:", "operations v2 failure strikes", "internal/database/pebble_store_ops_v2.go"},
	{"syslog:", "system log entries", "internal/database/pebble_store_activity.go"},

	// ── Playback, playlists, sync ──
	{"bookmark:", "bookmarks", "internal/database/pebble_store_bookmarks.go"},
	{"col:", "collections", "internal/database/pebble_store_collections.go"},
	{"playe:", "playback events", "internal/database/pebble_store_playback.go"},
	{"playlist:", "playlists", "internal/database/pebble_store_playlists.go"},
	{"playlistitem:", "playlist items", "internal/database/pebble_store_playlists.go"},
	{"playp:", "playback progress", "internal/database/pebble_store_playback.go"},
	{"sync_alias_use:", "sync alias use records", "internal/database/pebble_store_sync_alias_use.go"},
	{"sync_alias_use_seeded:", "sync alias use seeded markers", "internal/database/pebble_store_sync_alias_use.go"},
	{"sync_alias_use_seed_cutoff", "sync alias seed cutoff (single bare key)", "internal/database/pebble_store_sync_alias_use.go"},
	{"sync_file:", "sync file lookups", "internal/database/pebble_store_syncfile.go"},
	{"sync_item:", "sync item ids", "internal/database/pebble_store_syncid.go"},
	{"ubs:", "user book state", "internal/database/pebble_store_playback.go"},
	{"upl:", "user playlists", "internal/database/pebble_store_playlists.go"},
	{"upos:", "user playback positions per segment", "internal/database/pebble_store_playback.go"},

	// ── Review and tags ──
	{"review_item:", "review items and their indexes", "internal/database/review_store.go"},
	{"tag_idx:", "tag reverse index", "internal/database/pebble_store.go"},
}

// KeyFamilies returns a copy of the registry, sorted by prefix.
func KeyFamilies() []KeyFamily {
	out := make([]KeyFamily, len(keyFamilies))
	copy(out, keyFamilies)
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

// keyRange is one piece of the key-space partition. Lo is inclusive, Hi is
// exclusive; a nil Lo means "from the start", a nil Hi means "no upper bound".
type keyRange struct {
	Family string
	Lo, Hi []byte
}

// keyFamilyRanges partitions the WHOLE key space into sorted, gap-free,
// non-overlapping ranges. Each family's own range is
// [prefix, prefixUpperBound(prefix)) minus its direct children's ranges, so a
// parent can yield several pieces. Every gap between top-level families is a
// range with Family "(unregistered)", including the head [nil, first) and the
// tail [endOfLast, nil). Zero-width pieces are dropped.
func keyFamilyRanges(fams []KeyFamily) []keyRange {
	type node struct {
		prefix string
		kids   []*node
	}
	sorted := make([]string, 0, len(fams))
	seen := make(map[string]bool, len(fams))
	for _, f := range fams {
		if f.Prefix == "" || seen[f.Prefix] {
			continue
		}
		seen[f.Prefix] = true
		sorted = append(sorted, f.Prefix)
	}
	sort.Strings(sorted)

	nodes := make(map[string]*node, len(sorted))
	root := &node{prefix: unregisteredFamily}
	for _, p := range sorted {
		nodes[p] = &node{prefix: p}
	}
	// Ascending order means every child is attached after its parent and each
	// kids list stays sorted. The direct parent is the longest other
	// registered prefix that is a proper prefix of p.
	for _, p := range sorted {
		parent := root
		best := -1
		for _, q := range sorted {
			if q != p && len(q) > best && strings.HasPrefix(p, q) {
				parent, best = nodes[q], len(q)
			}
		}
		parent.kids = append(parent.kids, nodes[p])
	}

	var out []keyRange
	// emit walks one node. loInf/hiInf mark the open ends of the key space.
	var emit func(n *node, lo, hi []byte, hiInf bool)
	emit = func(n *node, lo, hi []byte, hiInf bool) {
		cursor, cursorInf := lo, false
		for _, k := range n.kids {
			klo := []byte(k.prefix)
			khi := prefixUpperBound(klo)
			if bytes.Compare(cursor, klo) < 0 {
				out = append(out, keyRange{Family: n.prefix, Lo: cursor, Hi: klo})
			}
			emit(k, klo, khi, khi == nil)
			cursor, cursorInf = khi, khi == nil
		}
		if cursorInf {
			return
		}
		if hiInf || bytes.Compare(cursor, hi) < 0 {
			out = append(out, keyRange{Family: n.prefix, Lo: cursor, Hi: hi})
		}
	}
	emit(root, nil, nil, true)
	return out
}

// familyForKey returns the family whose range holds key, by binary search over
// a partition built by keyFamilyRanges.
func familyForKey(ranges []keyRange, key []byte) string {
	i := sort.Search(len(ranges), func(i int) bool {
		return ranges[i].Hi == nil || bytes.Compare(key, ranges[i].Hi) < 0
	})
	if i >= len(ranges) {
		return unregisteredFamily
	}
	return ranges[i].Family
}

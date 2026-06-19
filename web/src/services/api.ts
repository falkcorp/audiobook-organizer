// file: web/src/services/api.ts
// version: 2.41.0
// guid: a0b1c2d3-e4f5-6789-abcd-ef0123456789
// last-edited: 2026-06-19

// API service layer for audiobook-organizer backend
// Provides typed functions for all backend endpoints

import { withOptimisticOperation } from '../utils/withOptimisticOperation';

const API_BASE = '/api/v1';

/**
 * wrapTrigger is the single seam every op-launching API call funnels through.
 * It inserts an optimistic placeholder into the ops store BEFORE the network
 * call, then reconciles it with the server's real op_id (which schedules a
 * loadFromServer refresh). The bell badge updates instantly; no UI handler
 * needs to remember to call loadFromServer or beginOptimistic.
 *
 * Use for every trigger* / start* function that enqueues a backend op.
 */
function wrapTrigger<T extends { id?: string; operation_id?: string }>(
  opName: string,
  fn: () => Promise<T>
): Promise<T> {
  return withOptimisticOperation(opName, fn, (r) => r?.id ?? r?.operation_id ?? null);
}

export class ApiError extends Error {
  readonly status: number;
  readonly data?: unknown;

  constructor(message: string, status: number, data?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.data = data;
  }
}

export interface DeleteBookResponse {
  message: string;
  blocked?: boolean;
  soft_delete?: boolean;
}

const buildApiError = async (response: Response, fallbackMessage: string) => {
  const data = await response.json().catch(() => ({}));
  const message =
    typeof (data as { error?: string }).error === 'string'
      ? (data as { error: string }).error
      : fallbackMessage;
  return new ApiError(message, response.status, data);
};

// Response types
export interface BookAuthorEntry {
  id: number;
  name: string;
  role: string;
  position: number;
}

export interface BookNarratorEntry {
  id: number;
  name: string;
  role: string;
  position: number;
}

export interface Book {
  id: string;
  title: string;
  author_id?: number;
  author_name?: string;
  series_id?: number;
  series_name?: string;
  series_position?: number;
  file_path: string;
  format?: string;
  duration?: number;
  narrator?: string;
  authors?: BookAuthorEntry[];
  narrators?: BookNarratorEntry[];
  language?: string;
  publisher?: string;
  description?: string;
  cover_image?: string;
  cover_url?: string;
  isbn?: string;
  isbn10?: string;
  isbn13?: string;
  asin?: string;
  open_library_id?: string;
  hardcover_id?: string;
  google_books_id?: string;
  work_id?: string;
  edition?: string;
  genre?: string;
  print_year?: number;
  audiobook_release_year?: number;
  original_filename?: string;
  bitrate?: number;
  codec?: string;
  sample_rate?: number;
  channels?: number;
  bit_depth?: number;
  quality?: string;
  is_primary_version?: boolean;
  version_group_id?: string;
  version_notes?: string;
  file_hash?: string;
  file_size?: number;
  original_file_hash?: string;
  organized_file_hash?: string;
  itunes_persistent_id?: string;
  itunes_date_added?: string;
  itunes_play_count?: number;
  itunes_last_played?: string;
  itunes_rating?: number;
  itunes_bookmark?: number;
  itunes_import_source?: string;
  itunes_path?: string;
  library_state?: string;
  quantity?: number;
  marked_for_deletion?: boolean;
  marked_for_deletion_at?: string;
  quarantine_reason?: string;
  quarantined_at?: string;
  organize_error?: string;
  metadata_provenance?: Record<string, TagSourceValues>;
  metadata_provenance_at?: string;
  created_at: string;
  updated_at: string;
  metadata_updated_at?: string;
  metadata_review_status?: string;
  last_written_at?: string;
  file_exists?: boolean;
  // User ratings (RATE-1/RATE-2)
  user_rating_overall?: number | null;
  user_rating_story?: number | null;
  user_rating_performance?: number | null;
  user_rating_notes?: string | null;
  // Audible runtime fields (DUR PR #549)
  audible_runtime_min?: number | null;
  duration_delta_sec?: number | null;
  // MATCH-1: metadata-source deduplication
  metadata_source_hash?: string | null;
  metadata_source_hash_duplicate_count?: number | null;
  // Book-level fingerprint signature (whole-file chromaprint synthesis)
  book_sig_v1?: string | null;
  book_sig_segments?: number | null;
  book_sig_built_at?: string | null;
  book_sig_v1_mask?: string | null;
  // 0–100 coverage % of real (non-synthesized) audio in the book sig.
  // null / undefined = full coverage (all files contributed real fingerprints).
  book_sig_coverage_pct?: number | null;
}

export interface Author {
  id: number;
  name: string;
  created_at: string;
}

export interface Series {
  id: number;
  name: string;
  author_id?: number;
  created_at: string;
}

export interface AuthorWithCount {
  id: number;
  name: string;
  book_count: number;
  file_count: number;
  aliases: AuthorAlias[];
}

export interface SeriesWithCount {
  id: number;
  name: string;
  author_id?: number;
  created_at: string;
  book_count: number;
  file_count: number;
  author_name?: string;
}

export interface Work {
  id: string;
  title: string;
  author_id?: number;
  series_id?: number;
  author_names?: string;
  alt_titles?: string[];
  description?: string;
  original_publish_year?: number;
  created_at?: string;
  updated_at?: string;
}

export interface TagSourceValues {
  file_value?: string | number | boolean | null;
  fetched_value?: string | number | boolean | null;
  stored_value?: string | number | boolean | null;
  override_value?: string | number | boolean | null;
  override_locked?: boolean;
  effective_value?: string | number | boolean | null;
  effective_source?: string;
  updated_at?: string;
}

export interface BookTags {
  media_info?: {
    codec?: string;
    bitrate?: number;
    sample_rate?: number;
    channels?: number;
    bit_depth?: number;
    quality?: string;
    duration?: number;
  };
  tags?: Record<string, TagSourceValues>;
}

/** @deprecated Use BookFile instead. Kept for backward compatibility with legacy endpoints. */
export interface BookSegment {
  id: string;
  file_path: string;
  format: string;
  size_bytes: number;
  duration_seconds: number;
  track_number?: number;
  total_tracks?: number;
  active: boolean;
  file_exists?: boolean;
}

export interface BookFile {
  id: string;
  book_id: string;
  file_path: string;
  original_filename?: string;
  itunes_path?: string;
  itunes_persistent_id?: string;
  track_number?: number;
  track_count?: number;
  disc_number?: number;
  disc_count?: number;
  title?: string;
  format?: string;
  codec?: string;
  duration?: number;
  file_size?: number;
  bitrate_kbps?: number;
  sample_rate_hz?: number;
  channels?: number;
  bit_depth?: number;
  file_hash?: string;
  missing: boolean;
  file_exists?: boolean;
  // Deluge import fields (DELUGE-1, PR #540)
  deluge_hash?: string | null;
  deluge_original_path?: string | null;
  imported_from_deluge_at?: string | null;
  created_at: string;
  updated_at: string;
}

export interface SegmentTags {
  segment_id: string;
  file_path: string;
  format: string;
  size_bytes: number;
  duration_sec: number;
  track_number?: number;
  total_tracks?: number;
  tags: Record<string, string>;
  used_filename_fallback: boolean;
  tags_read_error?: string;
}

export interface PathMapping {
  from: string;
  to: string;
}

export interface ITunesValidateRequest {
  library_path: string;
  path_mappings?: PathMapping[];
}

export interface ITunesValidateResponse {
  total_tracks: number;
  audiobook_tracks: number;
  audiobook_count: number;
  files_found: number;
  files_missing: number;
  missing_paths?: string[];
  path_prefixes?: string[];
  duplicate_count: number;
  estimated_import_time: string;
}

export interface ITunesImportRequest {
  library_path: string;
  import_mode: 'organized' | 'import' | 'organize';
  preserve_location: boolean;
  import_playlists: boolean;
  skip_duplicates: boolean;
  path_mappings?: PathMapping[];
}

export interface ITunesImportResponse {
  operation_id: string;
  status: string;
  message: string;
}

export interface ITunesWriteBackRequest {
  library_path: string;
  audiobook_ids: string[];
  create_backup: boolean;
  force_overwrite?: boolean;
}

export interface ITunesLibraryStatus {
  changed_since_import: boolean;
  fingerprint_stored: string;
  last_imported: string;
  last_external_change: string;
}

export interface ITunesWriteBackResponse {
  success: boolean;
  updated_count: number;
  backup_path?: string;
  message: string;
}

export interface ITunesImportStatus {
  operation_id: string;
  status: string;
  progress: number;
  message: string;
  total_books?: number;
  processed?: number;
  imported?: number;
  skipped?: number;
  failed?: number;
  errors?: string[];
}

export interface ImportPath {
  id: number;
  path: string;
  name: string;
  enabled: boolean;
  created_at: string;
  last_scan?: string;
  book_count: number;
}

export interface Operation {
  id: string;
  type: string;
  status: string;
  progress: number;
  total: number;
  message: string;
  folder_path?: string;
  created_at: string;
  started_at?: string;
  completed_at?: string;
  error_message?: string;
  errors?: string[];
  result_data?: string;
}

export interface SuggestionRole {
  name?: string;
  ids?: number[];
  variants?: string[];
  reason?: string;
}

export interface SuggestionRoles {
  author?: SuggestionRole;
  narrator?: SuggestionRole;
  publisher?: SuggestionRole;
}

export interface AIAuthorSuggestion {
  group_index: number;
  action: 'merge' | 'split' | 'rename' | 'skip' | 'alias' | 'reclassify';
  canonical_name: string;
  reason: string;
  confidence: 'high' | 'medium' | 'low';
  is_narrator?: number[];
  is_publisher?: number[];
  roles?: SuggestionRoles;
}

export interface ApplyAISuggestion {
  group_index: number;
  action: string;
  canonical_name: string;
  keep_id: number;
  merge_ids: number[];
  rename: boolean;
}

export interface OperationLog {
  id: number;
  operation_id: string;
  level: string;
  message: string;
  details?: string;
  created_at: string;
}

// Operations V2 (UOS-05: new timeline endpoint)
export interface OperationV2 {
  id: string;
  def_id: string;
  plugin: string;
  display_name: string;
  status:
    | 'queued'
    | 'running'
    | 'completed'
    | 'failed'
    | 'canceled'
    | 'interrupted_dropped'
    | 'interrupted_restart';
  priority: number;
  /** 0 = alert (bell + activity), 1 = activity only */
  notify_level: number;
  progress_current: number | null;
  progress_total: number | null;
  progress_message: string | null;
  current_phase: string | null;
  current_item: string | null;
  actor_user_id: string | null;
  parent_id: string | null;
  queued_at: string;
  started_at: string | null;
  completed_at: string | null;
  error_message: string | null;
  resume_count: number;
  trace_id: string | null;
  span_id: string | null;
}

export interface OperationTimelineResponse {
  operations: OperationV2[];
}

export async function getOperationTimeline(sinceMinutes = 15): Promise<OperationV2[]> {
  try {
    const response = await fetch(`${API_BASE}/operations/timeline?since=${sinceMinutes}m`);
    if (!response.ok) return [];
    const body = await response.json();
    return body?.data?.operations ?? [];
  } catch {
    return [];
  }
}

// SSE event types emitted by the operations EventHub (UOS-06).
export type OperationSSEEventName =
  | 'op.created'
  | 'op.updated'
  | 'op.log'
  | 'op.terminal'
  | 'op.current_item';

export interface OperationSSEHandler {
  onEvent: (name: OperationSSEEventName, payload: unknown) => void;
  onError?: (err: Event) => void;
}

/**
 * openOperationsSSE opens a Server-Sent Events connection to the operations
 * event stream. Returns an EventSource that the caller should close when
 * the component unmounts.
 *
 * The returned EventSource reconnects automatically on transient drops
 * (browser EventSource standard behaviour).
 */
export function openOperationsSSE(handler: OperationSSEHandler): EventSource {
  const url = `${API_BASE}/operations/events`;
  const es = new EventSource(url);

  const eventNames: OperationSSEEventName[] = [
    'op.created',
    'op.updated',
    'op.log',
    'op.terminal',
    'op.current_item',
  ];
  for (const name of eventNames) {
    es.addEventListener(name, (e: MessageEvent) => {
      try {
        const payload = JSON.parse(e.data);
        handler.onEvent(name, payload);
      } catch {
        handler.onEvent(name, e.data);
      }
    });
  }

  if (handler.onError) {
    es.onerror = handler.onError;
  }

  return es;
}

export interface SystemStatus {
  status: string;
  version?: string;
  library_book_count?: number;
  import_book_count?: number;
  total_book_count?: number;
  total_file_count?: number;
  author_count?: number;
  series_count?: number;
  library_size_bytes?: number;
  import_size_bytes?: number;
  total_size_bytes?: number;
  disk_total_bytes?: number;
  disk_used_bytes?: number;
  disk_free_bytes?: number;
  root_directory?: string;
  library: {
    book_count: number;
    folder_count: number;
    total_size: number;
    path?: string;
  };
  import_paths: {
    book_count: number;
    folder_count: number;
    total_size: number;
  };
  memory: {
    alloc_bytes: number;
    total_alloc_bytes: number;
    sys_bytes: number;
    num_gc: number;
    heap_alloc: number;
    heap_sys: number;
  };
  runtime: {
    go_version: string;
    num_goroutine: number;
    num_cpu: number;
    os: string;
    arch: string;
  };
  operations: {
    recent: Operation[];
  };
  app_uptime_seconds?: number;
  system_uptime_seconds?: number;
}

export interface SystemStorage {
  path: string;
  total_bytes: number;
  used_bytes: number;
  free_bytes: number;
  percent_used: number;
  quota_enabled: boolean;
  quota_percent: number;
  user_quotas_enabled: boolean;
}

export interface SystemLogs {
  logs: Array<{
    operation_id: string;
    timestamp: string;
    level: string;
    message: string;
    details?: string;
  }>;
  total: number;
  limit: number;
  offset: number;
}

export interface MetadataSource {
  id: string;
  name: string;
  enabled: boolean;
  priority: number;
  requires_auth: boolean;
  credentials: {
    [key: string]: string;
  };
}

export interface EmbeddingConfig {
  enabled: boolean;
  model: string;
  dimensions: number;
  base_url: string;
  vector_backend: string;
}

export interface DedupSignalConfig {
  band_certain_min: number;
  band_high_min: number;
  band_medium_min: number;
  band_review_min: number;
  duration_boost: number;
  folder_path_boost: number;
}

export interface DedupConfig {
  book_high_threshold: number;
  book_low_threshold: number;
  author_high_threshold: number;
  author_low_threshold: number;
  auto_merge_enabled: boolean;
  embeddings_enabled: boolean;
  llm_auto_merge_high_confidence: boolean;
  on_import_via_scheduler: boolean;
  review_model: string;
  signals: DedupSignalConfig;
}

export interface MetadataScoringConfig {
  embedding_enabled: boolean;
  embedding_min_score: number;
  embedding_best_match: number;
  llm_enabled: boolean;
  llm_rerank_epsilon: number;
  llm_rerank_top_k: number;
  write_backup_before: boolean;
}

export interface ITunesPathMap {
  from: string;
  to: string;
}

export interface ITunesConfig {
  sync_enabled: boolean;
  sync_interval: number;
  write_back_enabled: boolean;
  library_write_path: string;
  library_read_path: string;
  auto_write_back: boolean;
  path_trim_enabled: boolean;
  windows_root_path: string;
  media_root: string;
  path_mappings: ITunesPathMap[];
}

export interface MaintenanceConfig {
  enabled: boolean;
  window_start: number;
  window_end: number;
  dedup_refresh: boolean;
  series_prune: boolean;
  author_split: boolean;
  tombstone_cleanup: boolean;
  reconcile: boolean;
  purge_deleted: boolean;
  purge_old_logs: boolean;
  db_optimize: boolean;
  library_scan: boolean;
  library_organize: boolean;
  metadata_refresh: boolean;
  library_size_refresh: boolean;
  acoustid_online_lookup: boolean;
  acoustid_nightly_limit: number;
}

export interface ScheduledTaskConfig {
  enabled: boolean;
  interval: number;
  on_startup: boolean;
}

export interface ScheduledTasksConfig {
  dedup_refresh: ScheduledTaskConfig;
  author_split: ScheduledTaskConfig;
  db_optimize: ScheduledTaskConfig;
  metadata_refresh: ScheduledTaskConfig;
  resolve_production_authors: { enabled: boolean; interval: number };
  series_prune: ScheduledTaskConfig;
  ai_dedup_batch: ScheduledTaskConfig;
  reconcile: ScheduledTaskConfig;
}

export interface AutoUpdateConfig {
  enabled: boolean;
  channel: string;
  check_minutes: number;
  window_start: number;
  window_end: number;
}

export interface ToolsConfig {
  managed_dir: string;
  embed_queue_debounce_ms: number;
}

export interface Config {
  // Core paths
  root_dir: string;
  database_path: string;
  database_type: string;
  enable_sqlite: boolean;
  playlist_dir: string;
  setup_complete?: boolean;

  // Library organization
  organization_strategy: string;
  scan_on_startup: boolean;
  auto_organize: boolean;
  folder_naming_pattern: string;
  file_naming_pattern: string;
  create_backups: boolean;
  supported_extensions: string[];
  exclude_patterns?: string[];

  // Storage quotas
  enable_disk_quota: boolean;
  disk_quota_percent: number;
  enable_user_quotas: boolean;
  default_user_quota_gb: number;

  // Metadata
  auto_fetch_metadata: boolean;
  metadata_sources: MetadataSource[];
  language: string;

  // AI parsing
  enable_ai_parsing: boolean;
  metadata_llm_scoring_enabled?: boolean;
  openai_api_key: string;
  // Optional acoustid.org client ID for the acoustid.lookup-online op.
  // Masked when read back from the API; the user re-enters a new value
  // to change it. Empty value falls through to the ACOUSTID_API_KEY env.
  acoustid_api_key?: string;

  // Performance
  concurrent_scans: number;

  // Memory management
  memory_limit_type: string;
  cache_size: number;
  cache_invalidate_on_book_update: boolean;
  metadata_fetch_cache_ttl_days: number;
  memory_limit_percent: number;
  memory_limit_mb: number;

  // Lifecycle / retention
  purge_soft_deleted_after_days?: number;
  purge_soft_deleted_delete_files?: boolean;

  // Logging
  log_level: string;
  log_format: string;
  enable_json_logging: boolean;

  // Auto-update
  auto_update_enabled?: boolean;
  auto_update_channel?: string;
  auto_update_check_minutes?: number;
  auto_update_window_start?: number;
  auto_update_window_end?: number;

  // Maintenance window
  maintenance_window_enabled?: boolean;
  maintenance_window_start?: number;
  maintenance_window_end?: number;

  // Smart apply pipeline
  path_format?: string;
  segment_title_format?: string;
  auto_rename_on_apply?: boolean;
  auto_write_tags_on_apply?: boolean;
  verify_after_write?: boolean;

  // iTunes sync
  itunes_library_read_path?: string;
  itunes_library_write_path?: string;
  itl_write_back_enabled?: boolean;
  itunes_auto_write_back?: boolean;
  itunes_sync_enabled?: boolean;

  // Sub-structs (CFG-1 nested fields)
  embedding?: EmbeddingConfig;
  dedup?: DedupConfig;
  metadata_scoring?: MetadataScoringConfig;
  itunes?: ITunesConfig;
  maintenance?: MaintenanceConfig;
  scheduled?: ScheduledTasksConfig;
  auto_update?: AutoUpdateConfig;
  tools?: ToolsConfig;

  // Deluge integration
  protected_paths?: string[];

  // Legacy fields (Goodreads deprecated Dec 2020, removed)
  api_keys: Record<string, never>;
}

export interface AuthStatus {
  has_users: boolean;
  auth_enabled?: boolean;
  requires_auth: boolean;
  bootstrap_ready: boolean;
}

export interface AuthUser {
  id: string;
  username: string;
  email: string;
  roles: string[];
  status: string;
  created_at: string;
}

export interface AuthSession {
  id: string;
  user_id: string;
  expires_at: string;
  ip_address?: string;
  user_agent?: string;
  revoked?: boolean;
  created_at: string;
  current?: boolean;
}

// API functions

// Books
export interface BooksPage {
  items: Book[];
  count: number;
}

export async function getBooks(
  limit = 100,
  offset = 0,
  options?: {
    sortBy?: string;
    sortOrder?: string;
    tag?: string;
    tags?: string[];
    libraryState?: string;
    filters?: string;
    showFailed?: boolean;
    hasFileErrors?: boolean;
    fingerprintStatus?: 'complete' | 'partial' | 'none';
    coveragePercentMin?: number;
    coveragePercentMax?: number;
  }
): Promise<BooksPage> {
  const params = new URLSearchParams();
  params.set('limit', String(limit));
  params.set('offset', String(offset));
  if (options?.sortBy) params.set('sort_by', options.sortBy);
  if (options?.sortOrder) params.set('sort_order', options.sortOrder);
  if (options?.tags && options.tags.length > 0) {
    for (const t of options.tags) {
      params.append('tags[]', t);
    }
  } else if (options?.tag) {
    params.set('tag', options.tag);
  }
  if (options?.libraryState) params.set('library_state', options.libraryState);
  if (options?.filters) params.set('filters', options.filters);
  if (options?.showFailed) params.set('show_quarantined', 'true');
  if (options?.hasFileErrors) params.set('has_file_errors', 'true');
  if (options?.fingerprintStatus) params.set('fingerprint_status', options.fingerprintStatus);
  if (options?.coveragePercentMin !== undefined)
    params.set('coverage_percent_min', String(options.coveragePercentMin));
  if (options?.coveragePercentMax !== undefined)
    params.set('coverage_percent_max', String(options.coveragePercentMax));
  params.set('is_primary_version', 'true');

  const response = await fetch(`${API_BASE}/audiobooks?${params}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch books');
  }
  const body = await response.json();
  const data = body.data ?? body;
  return { items: data.items ?? [], count: data.count ?? 0 };
}

export interface BookFacets {
  genres: string[];
  languages: string[];
}

export async function getBookFacets(): Promise<BookFacets> {
  const response = await fetch(`${API_BASE}/audiobooks/facets`, {
    credentials: 'include',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch facets');
  }
  const body = await response.json();
  const data = body.data ?? body;
  return {
    genres: data.genres ?? [],
    languages: data.languages ?? [],
  };
}

export async function getBook(id: string): Promise<Book> {
  const response = await fetch(`${API_BASE}/audiobooks/${id}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch book');
  }
  const body = await response.json();
  return body.data;
}

export async function searchBooks(query: string, limit = 50, showFailed = false): Promise<Book[]> {
  let url = `${API_BASE}/audiobooks?search=${encodeURIComponent(query)}&limit=${limit}&is_primary_version=true`;
  if (showFailed) url += '&show_quarantined=true';
  const response = await fetch(url);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to search books');
  }
  const data = await response.json();
  return data.items || [];
}

export async function searchBooksPage(
  query: string,
  limit = 50,
  offset = 0,
  showFailed = false
): Promise<BooksPage> {
  const params = new URLSearchParams({
    search: query,
    limit: String(limit),
    offset: String(offset),
    is_primary_version: 'true',
  });
  if (showFailed) params.set('show_quarantined', 'true');
  const response = await fetch(`${API_BASE}/audiobooks?${params}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to search books');
  }
  const body = await response.json();
  const data = body.data ?? body;
  return { items: data.items ?? [], count: data.count ?? 0 };
}

export async function countBooks(): Promise<number> {
  const response = await fetch(`${API_BASE}/audiobooks/count`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to count books');
  }
  const body = await response.json();
  return body.data?.count || 0;
}

export async function countBooksFiltered(options: { libraryState?: string }): Promise<number> {
  const params = new URLSearchParams({ limit: '1', offset: '0' });
  if (options.libraryState) params.set('library_state', options.libraryState);
  const response = await fetch(`${API_BASE}/audiobooks?${params}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to count filtered books');
  }
  const body = await response.json();
  return body.data?.count || 0;
}

export async function getSoftDeletedBooks(
  limit = 100,
  offset = 0,
  olderThanDays?: number
): Promise<{ items: Book[]; count: number }> {
  const params = new URLSearchParams({
    limit: String(limit),
    offset: String(offset),
  });
  if (olderThanDays && olderThanDays > 0) {
    params.set('older_than_days', String(olderThanDays));
  }
  const response = await fetch(`${API_BASE}/audiobooks/soft-deleted?${params.toString()}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch soft-deleted books');
  }
  const body = await response.json();
  const data = body.data;
  return {
    items: data.items || [],
    count: data.total ?? data.count ?? (data.items ? data.items.length : 0),
  };
}

export async function purgeSoftDeletedBooks(
  deleteFiles = false,
  olderThanDays?: number
): Promise<{
  attempted: number;
  purged: number;
  files_deleted: number;
  errors: string[];
}> {
  const params = new URLSearchParams({
    delete_files: String(deleteFiles),
  });
  if (olderThanDays && olderThanDays > 0) {
    params.set('older_than_days', String(olderThanDays));
  }
  const response = await fetch(`${API_BASE}/audiobooks/purge-soft-deleted?${params.toString()}`, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to purge soft-deleted books');
  }
  const body = await response.json();
  return body.data;
}

export async function quarantineBook(bookId: string, reason?: string): Promise<void> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/quarantine`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ reason: reason || 'manually quarantined' }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to quarantine audiobook');
  }
}

export async function unquarantineBook(bookId: string): Promise<void> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/quarantine`, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to unquarantine audiobook');
  }
}

export async function restoreSoftDeletedBook(bookId: string): Promise<void> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/restore`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to restore audiobook');
  }
}

export interface RescanBookResult {
  book_id: string;
  old_total: number;
  new_total: number;
  file_count: number;
  files: Array<{
    id?: string;
    path: string;
    old_size: number;
    new_size: number;
    missing: boolean;
  }>;
}

export async function rescanBookFiles(bookId: string): Promise<RescanBookResult> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/rescan`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to rescan book files');
  }
  const json = await response.json();
  return json.data ?? json;
}

export async function deleteBook(
  bookId: string,
  options: { softDelete?: boolean; blockHash?: boolean } = {}
): Promise<DeleteBookResponse> {
  const params = new URLSearchParams();
  if (options.softDelete) params.set('soft_delete', 'true');
  if (options.blockHash) params.set('block_hash', 'true');
  const query = params.toString();
  const url =
    query.length > 0
      ? `${API_BASE}/audiobooks/${bookId}?${query}`
      : `${API_BASE}/audiobooks/${bookId}`;
  const response = await fetch(url, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to delete audiobook');
  }
  return response.json();
}

export type OverridePayload = {
  value?: unknown;
  locked?: boolean;
  fetched_value?: unknown;
  clear?: boolean;
};

export async function updateBook(
  bookId: string,
  updates: Partial<Book> & {
    overrides?: Record<string, OverridePayload>;
    unlock_overrides?: string[];
    force_update?: boolean;
  }
): Promise<Book> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(updates),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to update audiobook');
  }
  const body = await response.json();
  return body.data;
}

// RatingPatchBody is the partial-update payload for PATCH /api/v1/audiobooks/:id/rating.
// Omit a field to leave it unchanged. Pass null to clear it.
export interface RatingPatchBody {
  overall?: number | null;
  story?: number | null;
  performance?: number | null;
  notes?: string | null;
}

// patchAudiobookRating sends a partial rating update for a single book.
// Only fields present in body are touched; omitted fields are unchanged.
export async function patchAudiobookRating(bookId: string, body: RatingPatchBody): Promise<Book> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/rating`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to update rating');
  }
  return response.json();
}

// batchUpdateBooks applies the same metadata updates to every
// book in `ids` via a single API call. Much faster than N
// individual updateBook calls — one round trip and one DB
// write loop instead of N of each.
export interface BatchUpdateResult {
  updated: number;
  failed: number;
  errors?: string[];
}

export async function batchUpdateBooks(
  ids: string[],
  updates: Record<string, unknown>
): Promise<BatchUpdateResult> {
  const response = await fetch(`${API_BASE}/audiobooks/batch`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ids, updates }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to batch update audiobooks');
  }
  return response.json();
}

export async function getBookTags(
  bookId: string,
  compareId?: string,
  snapshotTimestamp?: string
): Promise<BookTags> {
  const params = new URLSearchParams();
  if (compareId) params.set('compare_id', compareId);
  if (snapshotTimestamp) params.set('snapshot_ts', snapshotTimestamp);
  const query = params.toString();
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/tags${query ? `?${query}` : ''}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch book tags');
  }
  return response.json();
}

export interface ChangeLogEntry {
  timestamp: string;
  type: 'tag_write' | 'rename' | 'metadata_apply' | 'import' | 'transcode';
  summary: string;
  details?: Record<string, unknown>;
}

export async function getBookChangelog(bookId: string): Promise<{ entries: ChangeLogEntry[] }> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/changelog`);
  if (!response.ok) return { entries: [] };
  const body = await response.json();
  return body.data;
}

/** @deprecated Use getBookFiles instead. This calls the legacy segments endpoint. */
export async function getBookSegments(bookId: string): Promise<BookSegment[]> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/segments`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch book segments');
  }
  const body = await response.json();
  return body.data;
}

export async function getBookFiles(
  bookId: string,
  options?: { limit?: number; offset?: number; signal?: AbortSignal }
): Promise<{ files: BookFile[]; count: number }> {
  const params = new URLSearchParams();
  if (options?.limit !== undefined) params.append('limit', String(options.limit));
  if (options?.offset !== undefined) params.append('offset', String(options.offset));
  const qs = params.toString();
  const url = `${API_BASE}/audiobooks/${bookId}/files${qs ? '?' + qs : ''}`;
  const response = await fetch(url, { signal: options?.signal });
  if (!response.ok) throw new Error(`Failed to fetch book files: ${response.status}`);
  const body = await response.json();
  return body.data;
}

export async function patchBookFile(
  bookId: string,
  fileId: string,
  patch: { skip_scan?: boolean }
): Promise<BookFile> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/files/${fileId}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to update file');
  }
  return response.json();
}

export async function getSegmentTags(bookId: string, segmentId: string): Promise<SegmentTags> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/segments/${segmentId}/tags`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch segment tags');
  }
  const body = await response.json();
  return body.data;
}

// Authors
export async function getAuthors(): Promise<Author[]> {
  const response = await fetch(`${API_BASE}/authors`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch authors');
  }
  const body = await response.json();
  const data = body.data;
  return data.items || data.authors || [];
}

export async function countAuthors(): Promise<number> {
  const response = await fetch(`${API_BASE}/authors/count`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to count authors');
  }
  const body = await response.json();
  const data = body.data;
  return data.count ?? 0;
}

export interface Announcement {
  id: string;
  severity: 'info' | 'warning' | 'error';
  message: string;
  link?: string;
}

export async function getAnnouncements(): Promise<Announcement[]> {
  const response = await fetch(`${API_BASE}/system/announcements`);
  if (!response.ok) return [];
  const data = await response.json();
  return data.announcements || [];
}

export interface AuthorDedupGroup {
  canonical: Author;
  variants: Author[];
  book_count: number;
  suggested_name?: string;
  split_names?: string[];
  is_production_company?: boolean;
}

export interface MergeAuthorsResult {
  merged: number;
  errors: string[];
}

export async function getAuthorDuplicates(): Promise<{
  groups: AuthorDedupGroup[];
  needs_refresh?: boolean;
}> {
  const response = await fetch(`${API_BASE}/authors/duplicates`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch author duplicates');
  }
  const body = await response.json();
  const data = body.data;
  return { groups: data.groups || [], needs_refresh: data.needs_refresh };
}

export async function refreshAuthorDuplicates(): Promise<Operation> {
  const response = await fetch(`${API_BASE}/authors/duplicates/refresh`, { method: 'POST' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to start author dedup scan');
  }
  const body = await response.json();
  return body.data;
}

export async function mergeAuthors(keepId: number, mergeIds: number[]): Promise<Operation> {
  const response = await fetch(`${API_BASE}/authors/merge`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ keep_id: keepId, merge_ids: mergeIds }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to merge authors');
  }
  const body = await response.json();
  return body.data;
}

export async function getBooksByAuthor(authorId: number): Promise<Book[]> {
  const response = await fetch(`${API_BASE}/audiobooks?author_id=${authorId}`);
  if (!response.ok) return [];
  const data = await response.json();
  return data.items || [];
}

export interface AuthorAlias {
  id: number;
  author_id: number;
  alias_name: string;
  alias_type: string;
  created_at: string;
}

export async function getAuthorAliases(authorId: number): Promise<AuthorAlias[]> {
  const response = await fetch(`${API_BASE}/authors/${authorId}/aliases`);
  if (!response.ok) return [];
  const body = await response.json();
  const data = body.data;
  return data.aliases || [];
}

export async function createAuthorAlias(
  authorId: number,
  aliasName: string,
  aliasType: string = 'alias'
): Promise<AuthorAlias> {
  const response = await fetch(`${API_BASE}/authors/${authorId}/aliases`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ alias_name: aliasName, alias_type: aliasType }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to create author alias');
  }
  const body = await response.json();
  return body.data;
}

export async function deleteAuthorAlias(authorId: number, aliasId: number): Promise<void> {
  const response = await fetch(`${API_BASE}/authors/${authorId}/aliases/${aliasId}`, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to delete author alias');
  }
}

export async function resolveProductionAuthor(authorId: number): Promise<Operation> {
  const response = await fetch(`${API_BASE}/authors/${authorId}/resolve-production`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to resolve production author');
  }
  const body = await response.json();
  return body.data.operation;
}

// Book dedup — uses existing /audiobooks/duplicates endpoint which returns Book[][] groups
export interface DuplicatesResponse {
  groups: Book[][];
  group_count: number;
  duplicate_count: number;
}

export interface MergeBooksResult {
  merged: number;
  errors: string[];
}

export async function getBookDuplicates(): Promise<DuplicatesResponse> {
  const response = await fetch(`${API_BASE}/audiobooks/duplicates`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch book duplicates');
  }
  const body = await response.json();
  return body.data;
}

export async function mergeBooks(keepId: string, mergeIds: string[]): Promise<Operation> {
  const response = await fetch(`${API_BASE}/audiobooks/merge`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ keep_id: keepId, merge_ids: mergeIds }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to merge books');
  }
  return response.json();
}

// Book dedup scan — advanced duplicate detection with confidence levels
export interface BookDedupGroup {
  books: Book[];
  confidence: 'high' | 'medium' | 'low';
  reason: string;
  group_key: string;
}

export interface BookDedupScanResponse {
  groups: BookDedupGroup[];
  group_count: number;
  duplicate_count: number;
  needs_refresh?: boolean;
}

export async function getBookDedupScanResults(): Promise<BookDedupScanResponse> {
  const response = await fetch(`${API_BASE}/audiobooks/duplicates/scan-results`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch book dedup scan results');
  }
  const body = await response.json();
  return body.data;
}

export async function scanBookDuplicates(): Promise<Operation> {
  const response = await fetch(`${API_BASE}/audiobooks/duplicates/scan`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to start book dedup scan');
  }
  const body = await response.json();
  return body.data;
}

export async function mergeBookDuplicatesAsVersions(
  bookIds: string[]
): Promise<{ message: string; version_group_id: string; primary_id: string }> {
  const response = await fetch(`${API_BASE}/audiobooks/duplicates/merge`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ book_ids: bookIds }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to merge book duplicates as versions');
  }
  const body = await response.json();
  return body.data;
}

export async function dismissBookDuplicateGroup(groupKey: string): Promise<{ message: string }> {
  const response = await fetch(`${API_BASE}/audiobooks/duplicates/dismiss`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ group_key: groupKey }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to dismiss duplicate group');
  }
  const body = await response.json();
  return body.data;
}

// Series
export async function getSeries(): Promise<SeriesWithCount[]> {
  const response = await fetch(`${API_BASE}/series`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch series');
  }
  const body = await response.json();
  const data = body.data;
  return data.items || data.series || [];
}

export async function countSeries(): Promise<number> {
  const response = await fetch(`${API_BASE}/series/count`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to count series');
  }
  const body = await response.json();
  const data = body.data;
  return data.count ?? 0;
}

export async function getSeriesBooks(seriesId: number): Promise<Book[]> {
  const response = await fetch(`${API_BASE}/series/${seriesId}/books`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch series books');
  }
  const body = await response.json();
  const data = body.data;
  return data.items || data.books || [];
}

export async function renameSeries(seriesId: number, name: string): Promise<void> {
  const response = await fetch(`${API_BASE}/series/${seriesId}/name`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to rename series');
  }
}

export async function splitSeries(
  seriesId: number,
  bookIds: string[]
): Promise<{ new_series_id: number; books_moved: number }> {
  const response = await fetch(`${API_BASE}/series/${seriesId}/split`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ book_ids: bookIds }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to split series');
  }
  const body = await response.json();
  return body.data;
}

export async function deleteSeries(seriesId: number): Promise<void> {
  const response = await fetch(`${API_BASE}/series/${seriesId}`, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to delete series');
  }
}

export async function getAuthorsWithCounts(): Promise<AuthorWithCount[]> {
  const response = await fetch(`${API_BASE}/authors`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch authors');
  }
  const body = await response.json();
  const data = body.data;
  return data.items || data.authors || [];
}

export async function getAuthorBooks(authorId: number): Promise<Book[]> {
  const response = await fetch(`${API_BASE}/authors/${authorId}/books`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch author books');
  }
  const body = await response.json();
  const data = body.data;
  return data.items || data.books || [];
}

export async function deleteAuthor(authorId: number): Promise<void> {
  const response = await fetch(`${API_BASE}/authors/${authorId}`, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to delete author');
  }
}

export async function bulkDeleteAuthors(
  ids: number[]
): Promise<{ deleted: number; skipped: number; errors: string[]; total: number }> {
  const response = await fetch(`${API_BASE}/authors/bulk-delete`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ids }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to bulk delete authors');
  }
  const body = await response.json();
  return body.data;
}

export async function bulkDeleteSeries(
  ids: number[]
): Promise<{ deleted: number; skipped: number; errors: string[]; total: number }> {
  const response = await fetch(`${API_BASE}/series/bulk-delete`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ids }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to bulk delete series');
  }
  const body = await response.json();
  return body.data;
}

// Works
export async function getWorks(): Promise<Work[]> {
  const response = await fetch(`${API_BASE}/works`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch works');
  }
  const body = await response.json();
  const data = body.data;
  return data.items || data.works || [];
}

// Import Paths
export async function getImportPaths(): Promise<ImportPath[]> {
  const response = await fetch(`${API_BASE}/import-paths`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch import paths');
  }
  const body = await response.json();
  const data = body.data;
  return data.importPaths || [];
}

export async function addImportPath(path: string, name: string): Promise<ImportPath> {
  const response = await fetch(`${API_BASE}/import-paths`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, name }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to add import path');
  }
  const body = await response.json();
  // Server returns { data: { importPath, scan_operation_id?: string } }
  const data = body.data;
  return (data.importPath ? data.importPath : data) as ImportPath;
}

// Detailed add returning scan operation id when auto-scan kicks off
export interface AddImportPathDetailedResponse {
  importPath: ImportPath;
  scan_operation_id?: string;
}

export async function addImportPathDetailed(
  path: string,
  name: string
): Promise<AddImportPathDetailedResponse> {
  const response = await fetch(`${API_BASE}/import-paths`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, name }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to add import path');
  }
  const body = await response.json();
  const data = body.data;
  if (data.importPath) {
    return {
      importPath: data.importPath,
      scan_operation_id: data.scan_operation_id,
    };
  }
  // Legacy shape fallback
  return { importPath: data };
}

// Operation status polling

export async function removeImportPath(id: number): Promise<void> {
  const response = await fetch(`${API_BASE}/import-paths/${id}`, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to remove import path');
  }
}

// SelectionSpec describes which books an operation targets.
// Either book_ids (explicit list) or filter (resolved server-side) must be set.
export interface SelectionSpec {
  book_ids?: string[];
  filter?: {
    search?: string;
    library_state?: string;
    tag?: string;
    tags?: string[];
    field_filters?: Array<{ field: string; value: string; negated: boolean }>;
    author_id?: number;
    series_id?: number;
  };
}

// startBulkMetadataFetch enqueues a v2 bulk metadata fetch operation.
// Pass a SelectionSpec with either book_ids (explicit page-level selection)
// or filter (cross-page selection, resolved server-side with IsPrimaryVersion=true).
// Returns the operation ID for polling via the bell.
export async function startBulkMetadataFetch(
  selection: SelectionSpec,
  options?: { prefer_audible?: boolean; skip_cached?: boolean }
): Promise<{ operation_id: string }> {
  return wrapTrigger('library.bulk-metadata-fetch', async () => {
    const response = await fetch(`${API_BASE}/operations/v2`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        def_id: 'library.bulk-metadata-fetch',
        params: { selection, ...options },
      }),
    });
    if (!response.ok) throw await buildApiError(response, 'Failed to start bulk metadata fetch');
    const body = await response.json();
    return body?.data ?? { operation_id: '' };
  });
}

// Operations
export async function startScan(
  folderPath?: string,
  priority?: number,
  forceUpdate?: boolean
): Promise<Operation> {
  return wrapTrigger('library.scan', async () => {
    const response = await fetch(`${API_BASE}/operations/scan`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        folder_path: folderPath,
        priority,
        force_update: forceUpdate,
      }),
    });
    if (!response.ok) throw await buildApiError(response, 'Failed to start scan');
    return (await response.json()).data;
  });
}

export async function startTranscode(
  bookId: string,
  opts?: { output_format?: string; bitrate?: number; keep_original?: boolean }
): Promise<Operation> {
  return wrapTrigger('library.transcode', async () => {
    const response = await fetch(`${API_BASE}/operations/transcode`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        book_id: bookId,
        output_format: opts?.output_format,
        bitrate: opts?.bitrate,
        keep_original: opts?.keep_original,
      }),
    });
    if (!response.ok) throw await buildApiError(response, 'Failed to start transcode');
    return (await response.json()).data;
  });
}

export async function getOperationStatus(id: string): Promise<Operation> {
  const response = await fetch(`${API_BASE}/operations/${id}/status`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch operation status');
  }
  const body = await response.json();
  return body.data;
}

// Poll an operation until it completes or fails. Calls onProgress with each update.
export async function pollOperation(
  id: string,
  onProgress?: (op: Operation) => void,
  intervalMs = 1000
): Promise<Operation> {
  while (true) {
    const op = await getOperationStatus(id);
    onProgress?.(op);
    if (op.status === 'completed' || op.status === 'failed' || op.status === 'cancelled') {
      return op;
    }
    await new Promise((r) => setTimeout(r, intervalMs));
  }
}

export interface OptimizeDatabaseResult {
  books_processed: number;
  authors_split: number;
  narrators_split: number;
}

export async function optimizeDatabase(): Promise<OptimizeDatabaseResult> {
  const response = await fetch(`${API_BASE}/operations/optimize-database`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to optimize database');
  }
  const body = await response.json();
  return body.data;
}

export async function getOperationLogs(id: string): Promise<OperationLog[]> {
  const response = await fetch(`${API_BASE}/operations/${id}/logs`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch operation logs');
  }
  const body = await response.json();
  const data = body.data ?? body;
  return data.items || data.logs || [];
}

export async function getOperationLogsTail(id: string, tail: number): Promise<OperationLog[]> {
  const response = await fetch(`${API_BASE}/operations/${id}/logs?tail=${tail}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch operation logs tail');
  }
  const data = await response.json();
  return data.items || data.logs || [];
}

export async function cancelOperation(id: string): Promise<void> {
  const response = await fetch(`${API_BASE}/operations/${id}`, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to cancel operation');
  }
}

export async function clearStaleOperations(): Promise<{ cleared: number }> {
  const response = await fetch(`${API_BASE}/operations/clear-stale`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to clear stale operations');
  }
  const body = await response.json();
  return body.data;
}

export async function listOperations(
  limit = 50,
  offset = 0
): Promise<{ items: Operation[]; total: number; limit: number; offset: number }> {
  const response = await fetch(`${API_BASE}/operations?limit=${limit}&offset=${offset}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch operations');
  }
  const body = await response.json();
  return body.data;
}

export async function deleteOperationHistory(status: string): Promise<{ deleted: number }> {
  const response = await fetch(
    `${API_BASE}/operations/history?status=${encodeURIComponent(status)}`,
    { method: 'DELETE' }
  );
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to delete operation history');
  }
  const body = await response.json();
  return body.data;
}

export interface OperationChange {
  id: string;
  operation_id: string;
  book_id: string;
  change_type: string;
  field_name: string;
  old_value: string;
  new_value: string;
  reverted_at: string | null;
  created_at: string;
}

export async function getOperationChanges(operationId: string): Promise<OperationChange[]> {
  const response = await fetch(`${API_BASE}/operations/${operationId}/changes`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch operation changes');
  }
  const data = await response.json();
  return data.changes || [];
}

export async function revertOperation(operationId: string): Promise<void> {
  const response = await fetch(`${API_BASE}/operations/${operationId}/revert`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to revert operation');
  }
}

export async function getBookChanges(bookId: string): Promise<OperationChange[]> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/changes`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch book changes');
  }
  const data = await response.json();
  return data.changes || [];
}

// System
export async function getSystemStatus(): Promise<SystemStatus> {
  const response = await fetch(`${API_BASE}/system/status`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch system status');
  }
  const body = await response.json();
  return body.data;
}

export async function getSystemStorage(): Promise<SystemStorage> {
  const response = await fetch(`${API_BASE}/system/storage`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch system storage');
  }
  const body = await response.json();
  return body.data;
}

export async function factoryReset(confirm: string): Promise<{ message: string }> {
  const response = await fetch(`${API_BASE}/system/factory-reset`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ confirm }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Factory reset failed');
  }
  const body = await response.json();
  return body.data;
}

// Organize operation
export async function startOrganize(
  folderPath?: string,
  priority?: number,
  bookIds?: string[],
  options?: { fetchMetadataFirst?: boolean; syncITunesFirst?: boolean }
): Promise<Operation> {
  return wrapTrigger('library.organize', async () => {
    const response = await fetch(`${API_BASE}/operations/organize`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        folder_path: folderPath,
        priority,
        book_ids: bookIds,
        fetch_metadata_first: options?.fetchMetadataFirst,
        sync_itunes_first: options?.syncITunesFirst,
      }),
    });
    if (!response.ok) throw await buildApiError(response, 'Failed to start organize');
    return (await response.json()).data;
  });
}

export async function getSystemLogs(params?: {
  level?: string;
  search?: string;
  limit?: number;
  offset?: number;
}): Promise<SystemLogs> {
  const query = new URLSearchParams();
  if (params?.level) query.append('level', params.level);
  if (params?.search) query.append('search', params.search);
  if (params?.limit) query.append('limit', params.limit.toString());
  if (params?.offset) query.append('offset', params.offset.toString());

  const response = await fetch(`${API_BASE}/system/logs?${query}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch system logs');
  }
  const body = await response.json();
  return body.data;
}

// Config
export async function getConfig(): Promise<Config> {
  const response = await fetch(`${API_BASE}/config`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch config');
  }
  const data = await response.json();
  return data.data?.config ?? data.config;
}

export async function updateConfig(updates: Partial<Config>): Promise<Config> {
  const response = await fetch(`${API_BASE}/config`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(updates),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to update config');
  }
  const data = await response.json();
  return data.data?.config ?? data.config;
}

// Auth
export async function getAuthStatus(): Promise<AuthStatus> {
  const response = await fetch(`${API_BASE}/auth/status`, {
    credentials: 'include',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch auth status');
  }
  const body = await response.json();
  return body.data;
}

export async function setupAdmin(payload: {
  username: string;
  password: string;
  email?: string;
}): Promise<{ message: string; user: AuthUser }> {
  const response = await fetch(`${API_BASE}/auth/setup`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to create admin account');
  }
  const body = await response.json();
  return body.data;
}

export async function login(payload: {
  username: string;
  password: string;
  remember_me?: boolean;
}): Promise<{ user: AuthUser; session: AuthSession }> {
  const response = await fetch(`${API_BASE}/auth/login`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to login');
  }
  const body = await response.json();
  return body.data;
}

export interface TempLoginTokenResponse {
  token: string;
  login_url: string;
  expires_at: string;
  user: { id: string; username: string };
  session_ttl_hours: number;
}

export async function createTempLoginToken(userID: string): Promise<TempLoginTokenResponse> {
  const response = await fetch(`${API_BASE}/auth/temp-tokens`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ user_id: userID }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to mint temp-login token');
  }
  const body = await response.json();
  return body.data ?? body;
}

export interface AdminUserSummary {
  id: string;
  username: string;
  email: string;
  status: string;
  roles: string[];
}

export async function listAdminUsers(): Promise<AdminUserSummary[]> {
  const response = await fetch(`${API_BASE}/users`, { credentials: 'include' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to list users');
  }
  const body = await response.json();
  const data = body.data ?? body;
  return data.users ?? data.items ?? data ?? [];
}

export async function getMe(): Promise<AuthUser> {
  const response = await fetch(`${API_BASE}/auth/me`, {
    credentials: 'include',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch current user');
  }
  const data = await response.json();
  return data.data?.user ?? data.user;
}

export async function updateMe(payload: { email: string }): Promise<AuthUser> {
  const response = await fetch(`${API_BASE}/auth/me`, {
    method: 'PATCH',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to update profile');
  }
  const body = await response.json();
  return body.data?.user ?? body.user;
}

export async function changePassword(payload: {
  current_password: string;
  new_password: string;
}): Promise<void> {
  const response = await fetch(`${API_BASE}/auth/me/password`, {
    method: 'PUT',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to change password');
  }
}

export async function logout(): Promise<void> {
  const response = await fetch(`${API_BASE}/auth/logout`, {
    method: 'POST',
    credentials: 'include',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to logout');
  }
}

export async function listSessions(): Promise<AuthSession[]> {
  const response = await fetch(`${API_BASE}/auth/sessions`, {
    credentials: 'include',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to list sessions');
  }
  const data = await response.json();
  return data.sessions || [];
}

export async function revokeSession(sessionId: string): Promise<void> {
  const response = await fetch(`${API_BASE}/auth/sessions/${sessionId}`, {
    method: 'DELETE',
    credentials: 'include',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to revoke session');
  }
}

// Version Management
export async function getBookVersions(bookId: string): Promise<Book[]> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/versions`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch book versions');
  }
  const body = await response.json();
  return body.data?.versions || [];
}

export async function linkBookVersion(bookId: string, otherBookId: string): Promise<void> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/versions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ other_id: otherBookId }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to link book version');
  }
}

export async function unlinkBookVersion(bookId: string, otherBookId: string): Promise<void> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/versions`, {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ other_id: otherBookId }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to unlink book version');
  }
}

export async function setPrimaryVersion(bookId: string): Promise<void> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/set-primary`, {
    method: 'PUT',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to set primary version');
  }
}

export async function getVersionGroup(groupId: string): Promise<Book[]> {
  const response = await fetch(`${API_BASE}/version-groups/${groupId}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch version group');
  }
  const body = await response.json();
  return body.data?.audiobooks || [];
}

// Split selected segments into a new version (new book in same version group)
export async function splitVersion(bookId: string, segmentIds: string[]): Promise<Book> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/split-version`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ segment_ids: segmentIds }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to split version');
  }
  const body = await response.json();
  return body.data;
}

// Split selected segments into independent new books (one per segment).
// Unlike splitVersion, new books are NOT version-linked to the source.
export async function splitSegmentsToBooks(
  bookId: string,
  segmentIds: string[]
): Promise<{ created_books: Book[]; count: number }> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/split-to-books`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ segment_ids: segmentIds }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to split segments to books');
  }
  const body = await response.json();
  return body.data;
}

// Move segments from one book to another (must be in same version group)
export async function moveSegments(
  bookId: string,
  segmentIds: string[],
  targetBookId: string
): Promise<void> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/move-segments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ segment_ids: segmentIds, target_book_id: targetBookId }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to move segments');
  }
}

// File Import
export async function importFile(filePath: string, organize = false): Promise<Book> {
  const response = await fetch(`${API_BASE}/import/file`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ file_path: filePath, organize }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to import file');
  }
  const body = await response.json();
  return body.data;
}

export async function validateITunesLibrary(
  payload: ITunesValidateRequest
): Promise<ITunesValidateResponse> {
  const response = await fetch(`${API_BASE}/itunes/validate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to validate iTunes library');
  }
  const body = await response.json();
  return body.data;
}

export interface ITunesTestMappingResponse {
  tested: number;
  found: number;
  examples: { title: string; path: string }[];
}

export async function testITunesPathMapping(
  libraryPath: string,
  from: string,
  to: string
): Promise<ITunesTestMappingResponse> {
  const response = await fetch(`${API_BASE}/itunes/test-mapping`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ library_path: libraryPath, from, to }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to test path mapping');
  }
  const body = await response.json();
  return body.data;
}

export async function importITunesLibrary(
  payload: ITunesImportRequest
): Promise<ITunesImportResponse> {
  const response = await fetch(`${API_BASE}/itunes/import`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to import iTunes library');
  }
  const body = await response.json();
  return body.data;
}

export async function writeBackITunesLibrary(
  payload: ITunesWriteBackRequest
): Promise<ITunesWriteBackResponse> {
  const response = await fetch(`${API_BASE}/itunes/write-back`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (response.status === 409) {
    const data = await response.json();
    throw new ApiError(data.message || 'Library modified', 409, data);
  }
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to write back iTunes library');
  }
  const body = await response.json();
  return body.data;
}

// ITunesBookMapping mirrors the backend ITunesBookMapping struct. Four
// path columns surface the full picture: what iTunes currently has, the
// local equivalent of that, where AO has the file on disk, and what AO
// would write back into iTunes. local_path is preserved as an alias of
// ao_path for callers that still read the older field name.
export interface ITunesBookMapping {
  book_id: string;
  title: string;
  author: string;
  itunes_persistent_id: string;
  itunes_path?: string;
  itunes_path_translated?: string;
  ao_path: string;
  ao_itunes_translated_path?: string;
  path_differs?: boolean;
  /** @deprecated Use ao_path. Kept for backwards compatibility during migration. */
  local_path: string;
}

export async function getITunesBooks(
  search?: string,
  limit?: number,
  offset?: number
): Promise<{ items: ITunesBookMapping[]; count: number }> {
  const params = new URLSearchParams();
  if (search) params.set('search', search);
  if (limit != null) params.set('limit', String(limit));
  if (offset != null) params.set('offset', String(offset));
  const response = await fetch(`${API_BASE}/itunes/books?${params.toString()}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch iTunes books');
  }
  const body = await response.json();
  return body.data;
}

// previewITunesWriteBack accepts an optional libraryPath. When omitted (or
// empty) the backend uses the configured ITunesLibraryReadPath — the dialog
// no longer asks the user for this on every preview because it's a
// configure-once value that lives in Settings.
export async function previewITunesWriteBack(
  libraryPath?: string,
  bookIds?: string[]
): Promise<{ items: ITunesBookMapping[]; total: number }> {
  const response = await fetch(`${API_BASE}/itunes/write-back/preview`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ library_path: libraryPath || undefined, book_ids: bookIds }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to preview write-back');
  }
  const body = await response.json();
  return body.data;
}

export async function startITunesSync(
  libraryPath?: string,
  force?: boolean
): Promise<{ operation_id: string; message: string }> {
  return wrapTrigger('itunes.sync', async () => {
    const response = await fetch(`${API_BASE}/itunes/sync`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ library_path: libraryPath, force: force ?? true }),
    });
    if (!response.ok) throw await buildApiError(response, 'Sync failed');
    return (await response.json()).data;
  });
}

export async function getITunesLibraryStatus(path: string): Promise<ITunesLibraryStatus> {
  const response = await fetch(
    `${API_BASE}/itunes/library-status?path=${encodeURIComponent(path)}`
  );
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch library status');
  }
  const body = await response.json();
  return body.data;
}

export async function getITunesImportStatus(operationId: string): Promise<ITunesImportStatus> {
  const response = await fetch(`${API_BASE}/itunes/import-status/${operationId}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch iTunes import status');
  }
  const body = await response.json();
  return body.data;
}

export async function getITunesImportStatusBulk(
  operationIds: string[]
): Promise<Record<string, ITunesImportStatus>> {
  const response = await fetch(`${API_BASE}/itunes/import-status/bulk`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ids: operationIds }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch bulk import status');
  }
  const data = await response.json();
  return data.data.statuses || {};
}

// Series dedup
export interface SeriesBookSummary {
  id: string;
  title: string;
  cover_url?: string;
}

export interface SeriesWithBooks extends Series {
  books?: SeriesBookSummary[];
  author_name?: string;
}

export interface SeriesDupGroup {
  name: string;
  count: number;
  series: SeriesWithBooks[];
  suggested_name?: string;
  match_type?: string;
}

export async function getSeriesDuplicates(): Promise<{
  groups: SeriesDupGroup[];
  count: number;
  total_series: number;
  needs_refresh?: boolean;
}> {
  const response = await fetch(`${API_BASE}/series/duplicates`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch series duplicates');
  }
  const body = await response.json();
  return body.data;
}

export async function refreshSeriesDuplicates(): Promise<Operation> {
  const response = await fetch(`${API_BASE}/series/duplicates/refresh`, { method: 'POST' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to start series dedup scan');
  }
  const body = await response.json();
  return body.data;
}

// Dedup validation
export interface ValidationResult {
  source: string;
  title: string;
  author: string;
  series?: string;
  series_position?: string;
  cover_url?: string;
  isbn?: string;
}

export async function validateDedupEntry(
  query: string,
  type: 'series' | 'author' | 'book' = 'series'
): Promise<{ results: ValidationResult[]; query: string; type: string }> {
  const response = await fetch(`${API_BASE}/dedup/validate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ query, type }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to validate dedup entry');
  }
  const body = await response.json();
  return body.data;
}

export interface SeriesDedupResult {
  merged: number;
  remaining_series: number;
  errors: string[];
}

export async function deduplicateSeries(): Promise<Operation> {
  const response = await fetch(`${API_BASE}/series/deduplicate`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to deduplicate series');
  }
  const body = await response.json();
  return body.data;
}

export async function mergeSeriesGroup(
  keepId: number,
  mergeIds: number[],
  customName?: string
): Promise<Operation> {
  const body: Record<string, unknown> = { keep_id: keepId, merge_ids: mergeIds };
  if (customName) body.custom_name = customName;
  const response = await fetch(`${API_BASE}/series/merge`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to merge series');
  }
  const respBody = await response.json();
  return respBody.data;
}

export interface SeriesPrunePreviewGroup {
  name: string;
  canonical_id: number;
  merge_ids: number[] | null;
  book_count: number;
  type: 'duplicate' | 'orphan';
}

export interface SeriesPrunePreview {
  groups: SeriesPrunePreviewGroup[];
  duplicate_count: number;
  orphan_count: number;
  total_count: number;
}

export async function seriesPrunePreview(): Promise<SeriesPrunePreview> {
  const response = await fetch(`${API_BASE}/series/prune/preview`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to get series prune preview');
  }
  const body = await response.json();
  return body.data;
}

export async function seriesPrune(): Promise<Operation> {
  const response = await fetch(`${API_BASE}/series/prune`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to prune series');
  }
  const body = await response.json();
  return body.data;
}

export async function updateSeriesName(id: number, name: string): Promise<Series> {
  const response = await fetch(`${API_BASE}/series/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to update series name');
  }
  const body = await response.json();
  return body.data;
}

// Metadata Fetching
export interface MetadataResult {
  title: string;
  author: string;
  description?: string;
  publisher?: string;
  publish_year?: number;
  isbn?: string;
  cover_url?: string;
  language?: string;
}

export interface MetadataCandidate {
  title: string;
  author: string;
  narrator?: string;
  series?: string;
  series_position?: string;
  year?: number;
  publisher?: string;
  isbn?: string;
  asin?: string;
  cover_url?: string;
  description?: string;
  duration_sec?: number;
  duration_delta_sec?: number;
  language?: string;
  source: string;
  score: number;
  /** Audible overall star rating (1–5 scale). Absent when not provided. */
  audible_rating_overall?: number;
  /** Number of Audible star ratings. */
  audible_rating_count?: number;
  /** Google Books average rating (1–5 scale). Absent when not provided. */
  google_rating_average?: number;
  /** Number of Google Books ratings. */
  google_rating_count?: number;
}

export interface SearchMetadataResponse {
  results: MetadataCandidate[];
  query: string;
  sources_tried?: string[];
  sources_failed?: Record<string, string>;
  /** METADATA-CACHED-MATCHER: true when the response came from the
   *  persistent metadata_cache:<id> Pebble row rather than a fresh
   *  source chain run. */
  from_cache?: boolean;
  /** True when the cache row is within MetadataCacheTTL (30 days). */
  is_fresh?: boolean;
  /** When the cache row was written. */
  fetched_at?: string;
}

export async function searchMetadata(
  title: string,
  author?: string
): Promise<{ results: MetadataResult[]; source: string }> {
  const params = new URLSearchParams({ title });
  if (author) params.append('author', author);

  const response = await fetch(`${API_BASE}/metadata/search?${params.toString()}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to search metadata');
  }
  const body = await response.json();
  return body.data;
}

export async function fetchBookMetadata(
  bookId: string
): Promise<{ message: string; book: Book; source: string }> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/fetch-metadata`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch metadata');
  }
  const body = await response.json();
  return body.data;
}

export async function searchMetadataForBook(
  bookId: string,
  query?: string,
  author?: string,
  narrator?: string,
  series?: string,
  useRerank?: boolean,
  // METADATA-CACHED-MATCHER: pass refresh=true to bypass the persistent
  // cache and force a fresh fetch chain. Default false (use cache).
  refresh?: boolean
): Promise<SearchMetadataResponse> {
  const body: {
    query: string;
    author?: string;
    narrator?: string;
    series?: string;
    use_rerank?: boolean;
  } = { query: query || '' };
  if (author) body.author = author;
  if (narrator) body.narrator = narrator;
  if (series) body.series = series;
  if (useRerank) body.use_rerank = true;
  const url = refresh
    ? `${API_BASE}/audiobooks/${bookId}/search-metadata?refresh=true`
    : `${API_BASE}/audiobooks/${bookId}/search-metadata`;
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to search metadata');
  }
  const responseBody = await response.json();
  return responseBody.data;
}

export async function applyMetadataCandidate(
  bookId: string,
  candidate: MetadataCandidate,
  fields?: string[],
  writeBack?: boolean
): Promise<{ message: string; book: Book; source: string }> {
  const payload: { candidate: MetadataCandidate; fields: string[]; write_back?: boolean } = {
    candidate,
    fields: fields || [],
  };
  if (writeBack !== undefined) {
    payload.write_back = writeBack;
  }
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/apply-metadata`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to apply metadata');
  }
  const body = await response.json();
  return body.data;
}

export async function markNoMatch(bookId: string): Promise<void> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/mark-no-match`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to mark as no match');
  }
}

export interface WriteBackMetadataResponse {
  message: string;
  written_count: number;
}

export interface BatchWriteBackError {
  book_id: string;
  error: string;
}

export interface BatchWriteBackResponse {
  operation_id: string;
  message?: string;
  book_count?: number;
  written?: number;
  written_files?: number;
  renamed?: number;
  organized?: number;
  failed?: number;
  errors?: BatchWriteBackError[];
}

export async function writeBackMetadata(
  bookId: string,
  segmentIds?: string[]
): Promise<WriteBackMetadataResponse> {
  const options: RequestInit = { method: 'POST' };
  if (segmentIds && segmentIds.length > 0) {
    options.headers = { 'Content-Type': 'application/json' };
    options.body = JSON.stringify({ segment_ids: segmentIds });
  }
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/write-back`, options);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to write metadata to files');
  }
  const body = await response.json();
  return body.data;
}

export async function batchWriteBackMetadata(
  bookIds: string[],
  organize = false,
  force = false
): Promise<BatchWriteBackResponse> {
  const response = await fetch(`${API_BASE}/audiobooks/batch-write-back`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ book_ids: bookIds, organize, force }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to write metadata to files');
  }
  const body = await response.json();
  // Server returns operation_id at top level; data key carries legacy fields.
  return { ...body.data, operation_id: body.data?.operation_id ?? body.operation_id };
}

// Bulk write-back (async operation for all/filtered books)
export interface BulkWriteBackFilter {
  library_state?: string;
  author_id?: string;
  series_id?: string;
}

export interface BulkWriteBackRequest {
  filter?: BulkWriteBackFilter;
  dry_run?: boolean;
  rename?: boolean;
}

export interface BulkWriteBackResponse {
  operation_id?: string;
  estimated_books: number;
  dry_run?: boolean;
  message?: string;
}

export async function bulkWriteBackMetadata(
  options: BulkWriteBackRequest = {}
): Promise<BulkWriteBackResponse> {
  const response = await fetch(`${API_BASE}/audiobooks/bulk-write-back`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(options),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to start bulk write-back');
  }
  const body = await response.json();
  return body.data;
}

// Extract track info from filenames
export interface ExtractTrackInfoResponse {
  updated: number;
  total: number;
  segments: BookSegment[];
}

export async function extractTrackInfo(bookId: string): Promise<ExtractTrackInfoResponse> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/extract-track-info`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to extract track info');
  }
  const body = await response.json();
  return body.data;
}

// File Relocation
export interface RelocateRequest {
  segment_id?: string;
  new_path?: string;
  folder_path?: string;
}

export interface RelocateResult {
  updated: number;
  errors?: string[];
}

export async function relocateBookFiles(
  bookId: string,
  req: RelocateRequest
): Promise<RelocateResult> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/relocate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to relocate files');
  }
  const body = await response.json();
  return body.data;
}

// Bulk Metadata Fetching
export interface BulkFetchMetadataResult {
  book_id: string;
  status: string;
  message?: string;
  applied_fields?: string[];
  fetched_fields?: string[];
}

export interface BulkFetchMetadataResponse {
  updated_count: number;
  total_count: number;
  results: BulkFetchMetadataResult[];
  source: string;
}

export async function bulkFetchMetadata(
  bookIds: string[],
  onlyMissing = true
): Promise<BulkFetchMetadataResponse> {
  const response = await fetch(`${API_BASE}/metadata/bulk-fetch`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ book_ids: bookIds, only_missing: onlyMissing }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to bulk fetch metadata');
  }
  const body = await response.json();
  return body.data;
}

// AI Parsing
export interface AIParseResult {
  title: string;
  author: string;
  series?: string;
  series_number?: number;
  narrator?: string;
  publisher?: string;
  year?: number;
  confidence: 'high' | 'medium' | 'low';
}

export async function parseFilenameWithAI(filename: string): Promise<{ metadata: AIParseResult }> {
  const response = await fetch(`${API_BASE}/ai/parse-filename`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ filename }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to parse filename with AI');
  }
  const body = await response.json();
  return body.data;
}

export async function testMetadataSource(
  sourceId: string,
  apiKey: string
): Promise<{ success: boolean; message?: string; error?: string }> {
  const response = await fetch(`${API_BASE}/metadata-sources/test`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ source_id: sourceId, api_key: apiKey }),
  });
  const body = await response.json();
  return body.data;
}

export async function testAIConnection(
  apiKey?: string
): Promise<{ success: boolean; message?: string; error?: string }> {
  const response = await fetch(`${API_BASE}/ai/test-connection`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: apiKey ? JSON.stringify({ api_key: apiKey }) : undefined,
  });
  if (!response.ok) {
    const data = await response.json();
    throw new Error(data.error || 'Connection test failed');
  }
  const body = await response.json();
  return body.data;
}

export async function parseAudiobookWithAI(
  bookId: string
): Promise<{ message: string; book: Book; confidence: string }> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/parse-with-ai`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to parse audiobook with AI');
  }
  const body = await response.json();
  return body.data;
}

// Filesystem Browsing
export interface FileSystemItem {
  name: string;
  path: string;
  is_dir: boolean;
  size?: number;
  mod_time?: number;
  excluded: boolean;
}

export interface FilesystemBrowseResult {
  path: string;
  items: FileSystemItem[];
  count: number;
  disk_info?: {
    exists: boolean;
    readable: boolean;
    writable: boolean;
    total_bytes?: number;
    free_bytes?: number;
    library_bytes?: number;
  };
}

export async function browseFilesystem(path: string): Promise<FilesystemBrowseResult> {
  const response = await fetch(`${API_BASE}/filesystem/browse?path=${encodeURIComponent(path)}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to browse filesystem');
  }
  const body = await response.json();
  return body.data;
}

/** Fetches the server user's home directory path. */
export async function getHomeDirectory(): Promise<string> {
  const response = await fetch(`${API_BASE}/filesystem/home`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch home directory');
  }
  const body = await response.json();
  return body.data.path as string;
}

export async function excludeFilesystemPath(
  path: string,
  reason?: string
): Promise<{ excluded: boolean; path: string; reason?: string }> {
  const response = await fetch(`${API_BASE}/filesystem/exclude`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, reason }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to exclude path');
  }
  const body = await response.json();
  return body.data;
}

export async function includeFilesystemPath(path: string): Promise<void> {
  const response = await fetch(`${API_BASE}/filesystem/exclude`, {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to remove exclusion');
  }
}

export interface BackupInfo {
  filename: string;
  path?: string;
  size: number;
  checksum?: string;
  database_type?: string;
  created_at: string;
  auto?: boolean;
  trigger?: string;
  status?: string;
}

export interface BackupListResponse {
  backups: BackupInfo[];
  count: number;
}

export async function createBackup(maxBackups?: number): Promise<BackupInfo> {
  const response = await fetch(`${API_BASE}/backup/create`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(typeof maxBackups === 'number' ? { max_backups: maxBackups } : {}),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to create backup');
  }
  const body = await response.json();
  return body.data;
}

export async function listBackups(): Promise<BackupListResponse> {
  const response = await fetch(`${API_BASE}/backup/list`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to list backups');
  }
  const body = await response.json();
  return body.data;
}

export async function restoreBackup(filename: string, verify = true): Promise<{ message: string }> {
  const response = await fetch(`${API_BASE}/backup/restore`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ backup_filename: filename, verify }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to restore backup');
  }
  const body = await response.json();
  return body.data;
}

export async function deleteBackup(filename: string): Promise<void> {
  const response = await fetch(`${API_BASE}/backup/${filename}`, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to delete backup');
  }
}

// Blocked Hashes Management
export interface BlockedHash {
  hash: string;
  reason: string;
  created_at: string;
}

export interface BlockedHashesResponse {
  items: BlockedHash[];
  total: number;
}

export async function getBlockedHashes(): Promise<BlockedHashesResponse> {
  const response = await fetch(`${API_BASE}/blocked-hashes`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch blocked hashes');
  }
  const body = await response.json();
  return body.data;
}

export async function addBlockedHash(
  hash: string,
  reason: string
): Promise<{ message: string; hash: string; reason: string }> {
  const response = await fetch(`${API_BASE}/blocked-hashes`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ hash, reason }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to add blocked hash');
  }
  const body = await response.json();
  return body.data;
}

export async function removeBlockedHash(hash: string): Promise<{ message: string; hash: string }> {
  const response = await fetch(`${API_BASE}/blocked-hashes/${hash}`, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to remove blocked hash');
  }
  const body = await response.json();
  return body.data;
}

// Metadata History
export interface MetadataChangeRecord {
  id: number;
  book_id: string;
  field: string;
  previous_value?: string;
  new_value?: string;
  change_type: string;
  source?: string;
  changed_at: string;
}

export async function getBookMetadataHistory(bookId: string): Promise<MetadataChangeRecord[]> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/metadata-history`);
  if (!response.ok) throw await buildApiError(response, 'Failed to fetch metadata history');
  const data = await response.json();
  return data.history || [];
}

export async function getFieldMetadataHistory(
  bookId: string,
  field: string
): Promise<MetadataChangeRecord[]> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/metadata-history/${field}`);
  if (!response.ok) throw await buildApiError(response, 'Failed to fetch field history');
  const data = await response.json();
  return data.history || [];
}

export async function undoLastApply(
  bookId: string
): Promise<{ message: string; undone_fields: string[] }> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/undo-last-apply`, {
    method: 'POST',
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to undo last apply');
  return response.json();
}

export async function undoMetadataChange(
  bookId: string,
  field: string
): Promise<{ message: string }> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/metadata-history/${field}/undo`, {
    method: 'POST',
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to undo change');
  return response.json();
}

// Batch metadata candidate types and functions

export interface CandidateBookInfo {
  id: string;
  title: string;
  author: string;
  file_path: string;
  itunes_path?: string;
  cover_url?: string;
  format?: string;
  duration_seconds?: number;
  file_size_bytes?: number;
  // Book's current language (ISO code or full name). Used by
  // the review dialog's language filter to hide candidates
  // whose language disagrees with the book's. Empty means
  // "unknown" — filter is a no-op for that row.
  language?: string;
}

export interface CandidateResult {
  book: CandidateBookInfo;
  candidate?: MetadataCandidate;
  status: 'matched' | 'no_match' | 'error' | 'rejected' | 'applied';
  error_message?: string;
}

export interface BatchFetchResponse {
  results: CandidateResult[];
  matched: number;
  no_match: number;
  errors: number;
  total: number;
  total_count: number;
  limit: number;
  offset: number;
  total_matched?: number;
  total_no_match?: number;
  total_errors?: number;
}

export interface BatchFetchRequest {
  book_ids?: string[];
  selection?: {
    book_ids?: string[];
    filter?: {
      search?: string;
      library_state?: string;
      tag?: string;
      only_unmatched?: boolean;
    };
  };
  only_unmatched?: boolean;
}

export async function batchFetchCandidates(
  req: BatchFetchRequest
): Promise<{ operation_id: string; book_count?: number; message?: string }> {
  const response = await fetch(`${API_BASE}/metadata/batch-fetch-candidates`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to start batch fetch');
  return response.json();
}

export async function getOperationResults(
  operationId: string,
  limit = 100,
  offset = 0
): Promise<BatchFetchResponse> {
  const params = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  const response = await fetch(`${API_BASE}/operations/${operationId}/results?${params}`);
  if (!response.ok) throw await buildApiError(response, 'Failed to get operation results');
  return response.json();
}

// MetadataFetchSummary is one row in the Resume Review picker —
// a completed metadata_candidate_fetch operation with its result
// breakdown so the user knows what they're about to review.
export interface MetadataFetchSummary {
  id: string;
  type: string;
  status: string;
  created_at: string;
  completed_at?: string;
  result_count: number;
  matched_count: number;
  no_match_count: number;
  error_count: number;
}

// getRecentMetadataFetches returns up to the last 10 completed
// metadata batch-fetch operations that have persisted results,
// newest first. Used by the Resume Review picker dialog so users
// can pick which fetch to review when multiple are outstanding —
// solves the scenario where someone fires a second fetch before
// reviewing the first.
export async function getRecentMetadataFetches(): Promise<MetadataFetchSummary[]> {
  const response = await fetch(`${API_BASE}/metadata/recent-fetches`);
  if (!response.ok) throw await buildApiError(response, 'Failed to list recent metadata fetches');
  const data = await response.json();
  return data.operations || [];
}

export async function getPendingReview(): Promise<{
  operation_id: string;
  total_books: number;
  message: string;
}> {
  const response = await fetch(`${API_BASE}/metadata/pending-review`, { method: 'POST' });
  if (!response.ok) throw await buildApiError(response, 'Failed to get pending review');
  return response.json();
}

// CachedMetadataEntry is one row from GET /audiobooks/metadata/cached.
// METADATA-CACHED-MATCHER: this list is the source of truth for the
// Review popup. Each entry includes book identity + cache freshness +
// review status so the UI can render without a second round-trip.
export interface CachedMetadataEntry {
  book_id: string;
  title: string;
  fetched_at: string;
  candidate_count: number;
  is_fresh: boolean;
  review_status: '' | 'pending' | 'matched' | 'no_match';
}

// listCachedCandidates returns the list of books that have a cached
// metadata-candidate set, optionally filtered by review status.
// METADATA-CACHED-MATCHER replacement for the legacy getPendingReview.
export async function listCachedCandidates(
  status?: 'pending' | 'matched'
): Promise<{ entries: CachedMetadataEntry[]; total: number }> {
  const qs = status ? `?status=${status}` : '';
  const response = await fetch(`${API_BASE}/audiobooks/metadata/cached${qs}`);
  if (!response.ok) throw await buildApiError(response, 'Failed to list cached candidates');
  const data = await response.json();
  return data.data ?? data;
}

// getCachedReviewResults returns a paginated CandidateResult list sourced
// from the persistent metadata cache. Replaces getOperationResults for the
// MetadataReviewDialog. Status values: "matched" (pending review),
// "no_match" (user rejected), "applied" (already applied).
export async function getCachedReviewResults(
  limit: number,
  offset: number
): Promise<{
  results: CandidateResult[];
  total_count: number;
  matched: number;
  no_match: number;
  errors: number;
  total_applied?: number;
}> {
  const response = await fetch(
    `${API_BASE}/audiobooks/metadata/cache/review?limit=${limit}&offset=${offset}`
  );
  if (!response.ok) throw await buildApiError(response, 'Failed to load cached review results');
  const data = await response.json();
  return data.data ?? data;
}

// batchApplyFromCache applies the highest-scored cached candidate for each
// book in book_ids. Cache-mode replacement for batchApplyCandidates.
export async function batchApplyFromCache(bookIds: string[]): Promise<{ applied: number }> {
  const response = await fetch(`${API_BASE}/audiobooks/metadata/batch-apply-cached`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ book_ids: bookIds }),
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to apply cached candidates');
  const data = await response.json();
  return data.data ?? data;
}

// clearMetadataNoMatch clears a book's MetadataReviewStatus back to null
// so it re-surfaces in the Review dialog. Inverse of markNoMatch.
export async function clearMetadataNoMatch(bookId: string): Promise<void> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/clear-no-match`, {
    method: 'POST',
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to clear no-match status');
}

// MetadataResultItem is one row in the unified metadata-results listing.
// `result_json` is a JSON-encoded CandidateResult and is only populated
// for statuses that have a stored fetch result (matched / no_match /
// applied / rejected / error). `unfetched` rows have status only.
export interface MetadataResultItem {
  book_id: string;
  status: 'matched' | 'no_match' | 'applied' | 'rejected' | 'error' | 'unfetched';
  result_json?: string;
  operation_id?: string;
  fetched_at?: string;
}

export interface MetadataResultsResponse {
  items: MetadataResultItem[];
  total: number;
  by_status: Record<string, number>;
  limit: number;
  offset: number;
}

// getMetadataResults returns the latest metadata-fetch result per book,
// optionally filtered by status. Single source of truth for the Library
// page filter toggles and the "Resume Review" entry point. Replaces the
// scattered logic that lived in getPendingReview + client-side filtering.
//
// Pass status=['matched'] to retrieve the same set the legacy Resume
// Review button used to surface. Pass no status filter to see every
// book that has been fetched. Pass includeUnfetched=true to also include
// books that have never been queried.
export async function getMetadataResults(
  opts: {
    status?: ('matched' | 'no_match' | 'applied' | 'rejected' | 'error' | 'unfetched')[];
    limit?: number;
    offset?: number;
    includeUnfetched?: boolean;
  } = {}
): Promise<MetadataResultsResponse> {
  const params = new URLSearchParams();
  for (const s of opts.status ?? []) params.append('status', s);
  if (opts.limit !== undefined) params.set('limit', String(opts.limit));
  if (opts.offset !== undefined) params.set('offset', String(opts.offset));
  if (opts.includeUnfetched) params.set('include_unfetched', 'true');
  const qs = params.toString();
  const url = qs
    ? `${API_BASE}/library/metadata-results?${qs}`
    : `${API_BASE}/library/metadata-results`;
  const response = await fetch(url);
  if (!response.ok) throw await buildApiError(response, 'Failed to list metadata results');
  const data = await response.json();
  return data.data ?? data;
}

export async function batchApplyCandidates(
  operationId: string,
  bookIds: string[]
): Promise<{ applied: number }> {
  const response = await fetch(`${API_BASE}/metadata/batch-apply-candidates`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ operation_id: operationId, book_ids: bookIds }),
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to apply candidates');
  return response.json();
}

export async function batchRejectCandidates(
  operationId: string,
  bookIds: string[]
): Promise<{ rejected: number }> {
  const response = await fetch(`${API_BASE}/metadata/batch-reject-candidates`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ operation_id: operationId, book_ids: bookIds }),
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to reject candidates');
  return response.json();
}

export async function batchUnrejectCandidates(
  operationId: string,
  bookIds: string[]
): Promise<{ unrejected: number }> {
  const response = await fetch(`${API_BASE}/metadata/batch-unreject-candidates`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ operation_id: operationId, book_ids: bookIds }),
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to unreject candidates');
  return response.json();
}

export async function revertToSnapshot(
  bookId: string,
  timestamp: string
): Promise<{ message: string; book: Book }> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/revert-metadata`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ timestamp }),
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to revert to version');
  return response.json();
}

export interface BookVersionEntry {
  book_id: string;
  timestamp: string;
  data: string;
}

export async function getBookCOWVersions(
  bookId: string,
  limit?: number
): Promise<BookVersionEntry[]> {
  const params = limit ? `?limit=${limit}` : '';
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/cow-versions${params}`);
  if (!response.ok) throw await buildApiError(response, 'Failed to fetch book versions');
  const data = await response.json();
  return data.versions || [];
}

export async function pruneBookVersions(
  bookId: string,
  keepCount: number
): Promise<{ pruned: number }> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/cow-versions/prune`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ keep_count: keepCount }),
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to prune versions');
  return response.json();
}

// Metadata Field States (provenance)
export interface MetadataFieldStateEntry {
  fetched_value?: unknown;
  override_value?: unknown;
  override_locked: boolean;
  updated_at: string;
}

export type MetadataFieldStates = Record<string, MetadataFieldStateEntry>;

export async function getAudiobookFieldStates(bookId: string): Promise<MetadataFieldStates> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/field-states`);
  if (!response.ok) throw await buildApiError(response, 'Failed to fetch field states');
  const body = await response.json();
  return body.data?.field_states || {};
}

// Version
export async function getAppVersion(): Promise<string> {
  try {
    const response = await fetch(`${API_BASE}/health`);
    if (response.ok) {
      const data = await response.json();
      return data.version || 'unknown';
    }
  } catch {
    // ignore
  }
  return 'unknown';
}

// Open Library Data Dumps
export interface OLDumpTypeStatus {
  filename?: string;
  date?: string;
  download_progress: number;
  import_progress: number;
  record_count: number;
  last_updated?: string;
}

export interface OLDownloadProgress {
  dump_type: string;
  status: 'idle' | 'downloading' | 'complete' | 'error';
  downloaded: number;
  total_size: number;
  error?: string;
  source?: string;
}

export interface OLUploadedFile {
  filename: string;
  size: number;
  mod_time: string;
}

export interface OLDumpStatus {
  enabled: boolean;
  status?: {
    editions: OLDumpTypeStatus;
    authors: OLDumpTypeStatus;
    works: OLDumpTypeStatus;
  };
  downloads?: Record<string, OLDownloadProgress>;
  uploaded_files?: Record<string, OLUploadedFile>;
}

export async function getOLDumpStatus(): Promise<OLDumpStatus> {
  const response = await fetch(`${API_BASE}/openlibrary/status`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to get OL dump status');
  }
  return response.json();
}

export async function startOLDumpDownload(
  types?: string[]
): Promise<{ message: string; types: string[] }> {
  const response = await fetch(`${API_BASE}/openlibrary/download`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ types: types || ['editions', 'authors', 'works'] }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to start OL dump download');
  }
  return response.json();
}

export async function startOLDumpImport(
  types?: string[]
): Promise<{ message: string; types: string[]; operation_id?: string }> {
  return wrapTrigger('openlibrary.import', async () => {
    const response = await fetch(`${API_BASE}/openlibrary/import`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ types: types || ['editions', 'authors', 'works'] }),
    });
    if (!response.ok) throw await buildApiError(response, 'Failed to start OL dump import');
    return response.json();
  });
}

export async function uploadOLDump(
  dumpType: string,
  file: File,
  onProgress?: (percent: number) => void
): Promise<{ message: string; type: string; size: number }> {
  const formData = new FormData();
  formData.append('type', dumpType);
  formData.append('file', file);

  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', `${API_BASE}/openlibrary/upload`);

    const progressHandler = onProgress
      ? (e: ProgressEvent) => {
          if (e.lengthComputable) {
            onProgress(Math.round((e.loaded / e.total) * 100));
          }
        }
      : null;

    if (progressHandler) {
      xhr.upload.addEventListener('progress', progressHandler);
    }

    xhr.onload = () => {
      if (progressHandler) {
        xhr.upload.removeEventListener('progress', progressHandler);
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(JSON.parse(xhr.responseText));
      } else {
        let msg = 'Failed to upload dump file';
        try {
          const body = JSON.parse(xhr.responseText);
          if (body.error) msg = body.error;
        } catch {
          /* ignore */
        }
        reject(new Error(msg));
      }
    };

    xhr.onerror = () => {
      if (progressHandler) {
        xhr.upload.removeEventListener('progress', progressHandler);
      }
      reject(new Error('Upload network error'));
    };
    xhr.send(formData);
  });
}

export async function deleteOLDumpData(): Promise<{ message: string }> {
  const response = await fetch(`${API_BASE}/openlibrary/data`, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to delete OL dump data');
  }
  return response.json();
}

// Update management

export interface UpdateInfo {
  current_version: string;
  latest_version: string;
  channel: string;
  update_available: boolean;
  release_url?: string;
  release_notes?: string;
  published_at?: string;
  last_checked: string;
}

export async function getUpdateStatus(): Promise<UpdateInfo> {
  const response = await fetch(`${API_BASE}/update/status`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to get update status');
  }
  const body = await response.json();
  return body.data;
}

export async function checkForUpdate(): Promise<UpdateInfo> {
  const response = await fetch(`${API_BASE}/update/check`, { method: 'POST' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to check for updates');
  }
  const body = await response.json();
  return body.data;
}

export async function applyUpdate(): Promise<void> {
  const response = await fetch(`${API_BASE}/update/apply`, { method: 'POST' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to apply update');
  }
}

export async function splitCompositeAuthor(
  authorId: number,
  names?: string[]
): Promise<{ authors: { id: number; name: string }[]; books_updated: number }> {
  const response = await fetch(`${API_BASE}/authors/${authorId}/split`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(names ? { names } : {}),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to split author');
  }
  const body = await response.json();
  return body.data;
}

export async function renameAuthor(
  authorId: number,
  name: string
): Promise<{ id: number; name: string }> {
  const response = await fetch(`${API_BASE}/authors/${authorId}/name`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to rename author');
  }
  const body = await response.json();
  return body.data;
}

export async function reclassifyAuthorAsNarrator(
  authorId: number
): Promise<{ narrator_id: number; books_updated: number }> {
  const response = await fetch(`${API_BASE}/authors/${authorId}/reclassify-as-narrator`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to reclassify author as narrator');
  }
  const body = await response.json();
  return body.data;
}

// --- Unified Task/Scheduler API ---

export interface TaskInfo {
  name: string;
  description: string;
  category: string;
  enabled: boolean;
  interval_minutes: number;
  run_on_startup: boolean;
  run_in_maintenance_window: boolean;
  last_run?: string;
  is_running: boolean;
}

export async function getRegisteredTasks(): Promise<TaskInfo[]> {
  const response = await fetch(`${API_BASE}/tasks`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch tasks');
  }
  const body = await response.json();
  return Array.isArray(body) ? body : (body?.data ?? []);
}

export async function runTask(name: string): Promise<Operation | { message: string }> {
  const response = await fetch(`${API_BASE}/tasks/${name}/run`, { method: 'POST' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to run task');
  }
  return response.json();
}

export async function runMaintenanceWindow(): Promise<void> {
  const response = await fetch(`${API_BASE}/maintenance-window/run`, { method: 'POST' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to run maintenance window');
  }
}

export async function updateTaskConfig(
  name: string,
  updates: {
    enabled?: boolean;
    interval_minutes?: number;
    run_on_startup?: boolean;
    run_in_maintenance_window?: boolean;
  }
): Promise<void> {
  const response = await fetch(`${API_BASE}/tasks/${name}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(updates),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to update task config');
  }
}

export interface MaintenanceWindowStatus {
  enabled: boolean;
  window_start: number;
  window_end: number;
  last_run_date: string;
  next_run_estimate: string;
  currently_running: boolean;
}

export interface MaintenanceWindowConfig {
  enabled: boolean;
  window_start: number;
  window_end: number;
}

export async function getMaintenanceWindowStatus(): Promise<MaintenanceWindowStatus> {
  const response = await fetch(`${API_BASE}/maintenance-window/status`);
  if (!response.ok)
    throw await buildApiError(response, 'Failed to fetch maintenance window status');
  return response.json();
}

export async function updateMaintenanceWindowConfig(cfg: MaintenanceWindowConfig): Promise<void> {
  const response = await fetch(`${API_BASE}/maintenance-window/config`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(cfg),
  });
  if (!response.ok)
    throw await buildApiError(response, 'Failed to update maintenance window config');
}

// AI Author Review
export type AIReviewMode = 'full' | 'groups';

export async function requestAIAuthorReview(mode: AIReviewMode = 'groups'): Promise<Operation> {
  const response = await fetch(`${API_BASE}/authors/duplicates/ai-review`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ mode }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to start AI author review');
  }
  const body = await response.json();
  return body.data;
}

export async function getOperationResult(id: string): Promise<{ result_data: unknown }> {
  const response = await fetch(`${API_BASE}/operations/${id}/result`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to get operation result');
  }
  return response.json();
}

export async function applyAIAuthorReview(suggestions: ApplyAISuggestion[]): Promise<Operation> {
  const response = await fetch(`${API_BASE}/authors/duplicates/ai-review/apply`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ suggestions }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to apply AI author review');
  }
  const body = await response.json();
  return body.data;
}

// --- AI Scan Pipeline Types ---

export interface AIScan {
  id: number;
  status:
    | 'pending'
    | 'scanning'
    | 'enriching'
    | 'cross_validating'
    | 'complete'
    | 'failed'
    | 'canceled';
  mode: 'batch' | 'realtime';
  models: { groups: string; full: string };
  author_count: number;
  created_at: string;
  completed_at?: string;
}

export interface AIScanPhase {
  scan_id: number;
  phase_type: string;
  status: string;
  batch_id?: string;
  model: string;
  started_at?: string;
  completed_at?: string;
}

export interface AIScanResult {
  id: number;
  scan_id: number;
  agreement: 'agreed' | 'groups_only' | 'full_only' | 'disagreed';
  suggestion: {
    action: string;
    canonical_name: string;
    reason: string;
    confidence: string;
    author_ids?: number[];
    roles?: SuggestionRoles;
    source: string;
  };
  applied: boolean;
  applied_at?: string;
}

export interface AIScanDetail extends AIScan {
  phases: AIScanPhase[];
}

export interface AIScanComparison {
  new_in_b: AIScanResult[];
  resolved_from_a: AIScanResult[];
  unchanged: AIScanResult[];
}

// --- AI Scan Pipeline API Functions ---

export async function startAIScan(mode: 'batch' | 'realtime'): Promise<AIScan> {
  const response = await fetch(`${API_BASE}/ai/scans`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ mode }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to start AI scan');
  }
  const body = await response.json();
  return body.data;
}

export async function listAIScans(): Promise<AIScan[]> {
  const response = await fetch(`${API_BASE}/ai/scans`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to list AI scans');
  }
  const body = await response.json();
  const data = body.data;
  return data.scans || [];
}

export async function getAIScan(id: number): Promise<AIScanDetail> {
  const response = await fetch(`${API_BASE}/ai/scans/${id}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to get AI scan');
  }
  const body = await response.json();
  const data = body.data;
  return { ...data.scan, phases: data.phases || [] };
}

export async function getAIScanResults(id: number, agreement?: string): Promise<AIScanResult[]> {
  const params = agreement ? `?agreement=${agreement}` : '';
  const response = await fetch(`${API_BASE}/ai/scans/${id}/results${params}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to get scan results');
  }
  const body = await response.json();
  const data = body.data;
  return data.results || [];
}

export async function applyAIScanResults(
  scanID: number,
  resultIDs: number[]
): Promise<{ applied: number; errors: string[] }> {
  const response = await fetch(`${API_BASE}/ai/scans/${scanID}/apply`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ result_ids: resultIDs }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to apply scan results');
  }
  const body = await response.json();
  return body.data;
}

export async function cancelAIScan(id: number): Promise<void> {
  const response = await fetch(`${API_BASE}/ai/scans/${id}/cancel`, { method: 'POST' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to cancel scan');
  }
}

export async function deleteAIScan(id: number): Promise<void> {
  const response = await fetch(`${API_BASE}/ai/scans/${id}`, { method: 'DELETE' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to delete scan');
  }
}

export async function compareAIScans(a: number, b: number): Promise<AIScanComparison> {
  const response = await fetch(`${API_BASE}/ai/scans/compare?a=${a}&b=${b}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to compare scans');
  }
  const body = await response.json();
  return body.data;
}

// --- Rename Preview & Apply ---

export interface TagChange {
  field: string;
  current: string;
  proposed: string;
}

export interface RenamePreview {
  book_id: string;
  current_path: string;
  proposed_path: string;
  tag_changes: TagChange[];
}

export interface RenameApplyResult {
  book_id: string;
  old_path: string;
  new_path: string;
  tags_written: number;
  message: string;
}

export async function previewRename(bookId: string): Promise<RenamePreview> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/rename/preview`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to preview rename');
  }
  const body = await response.json();
  return body.data;
}

export async function applyRename(bookId: string): Promise<RenameApplyResult> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/rename/apply`, { method: 'POST' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to apply rename');
  }
  const body = await response.json();
  return body.data;
}

// ---- Organize Preview & Execute ----

export interface OrganizePreviewStep {
  action: string;
  description: string;
  from?: string;
  to?: string;
  files?: string[];
  tags?: Record<string, unknown>;
  cover_url?: string;
  warning?: string;
}

export interface OrganizePreviewResponse {
  steps: OrganizePreviewStep[];
  needs_copy: boolean;
  needs_rename: boolean;
  current_path: string;
  target_path: string;
  is_protected: boolean;
  has_book_files: boolean;
  book_file_count: number;
}

export interface OrganizeResult {
  message: string;
  book_id: string;
  old_path: string;
  new_path: string;
  tags_written: number;
  operation_id: string;
}

export async function previewOrganize(bookId: string): Promise<OrganizePreviewResponse> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/preview-organize`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to preview organize');
  }
  const body = await response.json();
  return body.data;
}

export async function organizeBook(bookId: string): Promise<OrganizeResult> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/organize`, { method: 'POST' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to organize book');
  }
  const body = await response.json();
  return body.data;
}

// ---- Reconciliation ----

export interface ReconcileMatch {
  book_id: string;
  book_title: string;
  old_path: string;
  new_path: string;
  match_type: 'hash' | 'original_hash' | 'filename';
  confidence: 'high' | 'medium' | 'low';
  score: number;
}

export interface ReconcileBrokenRecord {
  book_id: string;
  title: string;
  file_path: string;
  file_hash?: string;
}

export interface ReconcilePreview {
  broken_records: ReconcileBrokenRecord[];
  untracked_files: string[];
  matches: ReconcileMatch[];
  unmatched_books: ReconcileBrokenRecord[];
}

export async function getReconcilePreview(): Promise<ReconcilePreview> {
  const response = await fetch(`${API_BASE}/operations/reconcile/preview`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to get reconcile preview');
  }
  return response.json();
}

export async function startReconcile(
  matches: Array<{ book_id: string; new_path: string }>
): Promise<Operation> {
  return wrapTrigger('library.reconcile', async () => {
    const response = await fetch(`${API_BASE}/operations/reconcile`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ matches }),
    });
    if (!response.ok) throw await buildApiError(response, 'Failed to start reconciliation');
    return response.json();
  });
}

export async function startReconcileScan(): Promise<Operation> {
  return wrapTrigger('library.reconcile-scan', async () => {
    const response = await fetch(`${API_BASE}/operations/reconcile/scan`, {
      method: 'POST',
    });
    if (!response.ok) throw await buildApiError(response, 'Failed to start reconcile scan');
    const responseData = await response.json();
    const raw = responseData.data ?? responseData;
    return { ...raw, id: raw.id ?? raw.op_id ?? '' } as Operation;
  });
}

export interface LatestReconcileScan {
  operation: Operation | null;
  preview: ReconcilePreview | null;
}

export async function getLatestReconcileScan(): Promise<LatestReconcileScan> {
  const response = await fetch(`${API_BASE}/operations/reconcile/scan/latest`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to get latest reconcile scan');
  }
  return response.json();
}

// Diagnostics
export interface DiagnosticsSuggestion {
  id: string;
  action: 'merge_versions' | 'delete_orphan' | 'fix_metadata' | 'reassign_series';
  book_ids: string[];
  primary_id?: string;
  reason: string;
  fix?: Record<string, unknown>;
  applied: boolean;
}

export interface DiagnosticsAIResults {
  status: string;
  schema_version?: number;
  suggestions: DiagnosticsSuggestion[];
  raw_responses: unknown[];
}

export async function startDiagnosticsExport(
  category: string,
  description: string
): Promise<{ operation_id: string; status: string }> {
  return wrapTrigger('diagnostics.export', async () => {
    const response = await fetch(`${API_BASE}/diagnostics/export`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ category, description }),
    });
    if (!response.ok) throw await buildApiError(response, 'Failed to start export');
    return (await response.json()).data;
  });
}

export async function downloadDiagnosticsExport(operationId: string): Promise<Blob> {
  const response = await fetch(`${API_BASE}/diagnostics/export/${operationId}/download`);
  if (!response.ok) throw await buildApiError(response, 'Failed to download export');
  return response.blob();
}

export async function submitDiagnosticsAI(
  category: string,
  description: string
): Promise<{ operation_id: string; batch_id: string; status: string; request_count: number }> {
  const response = await fetch(`${API_BASE}/diagnostics/submit-ai`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ category, description }),
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to submit AI analysis');
  const body = await response.json();
  return body.data;
}

export async function getDiagnosticsAIResults(operationId: string): Promise<DiagnosticsAIResults> {
  const response = await fetch(`${API_BASE}/diagnostics/ai-results/${operationId}`);
  if (!response.ok) throw await buildApiError(response, 'Failed to get AI results');
  const body = await response.json();
  return body.data;
}

export async function applyDiagnosticsSuggestions(
  operationId: string,
  approvedIds: string[]
): Promise<{ applied: number; failed: number; errors: string[] }> {
  const response = await fetch(`${API_BASE}/diagnostics/apply-suggestions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ operation_id: operationId, approved_suggestion_ids: approvedIds }),
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to apply suggestions');
  const body = await response.json();
  return body.data;
}

export interface DBHealthTableStat {
  name: string;
  row_count: number;
}

export interface DBHealthStats {
  sqlite?: {
    tables: DBHealthTableStat[];
    size_bytes: number;
  };
  pebble?: {
    key_count: number;
    size_bytes: number;
  };
  embeddings: {
    vector_count: number;
    size_bytes: number;
  };
  ai_scans: {
    job_count: number;
    pending_count: number;
    size_bytes: number;
  };
  metadata_cache: {
    total_entries: number;
    ttl_days: number;
    expired_entries: number;
  };
  book_path_prefixes?: Array<{ prefix: string; book_count: number }>;
}

export async function getDBHealthStats(): Promise<DBHealthStats> {
  const response = await fetch(`${API_BASE}/diagnostics/db-health`);
  if (!response.ok) throw await buildApiError(response, 'Failed to fetch DB health stats');
  const body = await response.json();
  return body.data;
}

// AI Jobs
export interface AIJob {
  id: string;
  type: string;
  batch_id?: string;
  custom_id_prefix: string;
  status: string;
  item_count: number;
  success_count: number;
  error_count: number;
  row_errors?: string;
  error_msg?: string;
  submitted_at?: string;
  completed_at?: string;
  created_at: string;
}

export async function listAIJobs(params?: {
  type?: string;
  status?: string;
  limit?: number;
  offset?: number;
}): Promise<AIJob[]> {
  const qs = new URLSearchParams();
  if (params?.type) qs.set('type', params.type);
  if (params?.status) qs.set('status', params.status);
  if (params?.limit) qs.set('limit', String(params.limit));
  if (params?.offset) qs.set('offset', String(params.offset));
  const url = qs.toString() ? `${API_BASE}/ai-jobs?${qs}` : `${API_BASE}/ai-jobs`;
  const response = await fetch(url);
  if (!response.ok) throw await buildApiError(response, 'Failed to fetch AI jobs');
  const body = await response.json();
  return body.data?.jobs ?? [];
}

// External ID mappings
export interface ExternalIDMapping {
  id: number;
  source: string;
  external_id: string;
  book_id: string;
  track_number?: number;
  file_path?: string;
  tombstoned: boolean;
  created_at: string;
  updated_at: string;
}

export async function getBookExternalIDs(bookId: string): Promise<{
  external_ids: ExternalIDMapping[];
  itunes_linked: boolean;
  total: number;
}> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/external-ids`);
  if (!response.ok) return { external_ids: [], itunes_linked: false, total: 0 };
  return response.json();
}

// --- User tag API functions ---

export async function getBookUserTags(bookId: string): Promise<string[]> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/user-tags`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to get book tags');
  }
  const data = await response.json();
  return data.tags;
}

export async function setBookUserTags(bookId: string, tags: string[]): Promise<string[]> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/user-tags`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tags }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to set book tags');
  }
  const data = await response.json();
  return data.tags;
}

export async function addBookUserTag(bookId: string, tag: string): Promise<string[]> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/user-tags`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tag }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to add book tag');
  }
  const data = await response.json();
  return data.tags;
}

export async function removeBookUserTag(bookId: string, tag: string): Promise<string[]> {
  const response = await fetch(
    `${API_BASE}/audiobooks/${bookId}/user-tags/${encodeURIComponent(tag)}`,
    { method: 'DELETE' }
  );
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to remove book tag');
  }
  const data = await response.json();
  return data.tags;
}

// DetailedBookTag carries the source attribution alongside the
// tag string. `source='user'` is a human-applied label; everything
// else (`system`, potentially future sources) is server-applied
// provenance and should be rendered as non-deletable in the UI.
export interface DetailedBookTag {
  tag: string;
  source: string;
  created_at: string;
}

// getBookTagsDetailed returns tags with their source attribution
// so the frontend can render user-applied and system-applied
// tags differently. System tags follow the namespace from
// migrations 47/48 — dedup:*, metadata:source:*, metadata:language:*,
// etc. — and are the result of automatic server-side actions.
export async function getBookTagsDetailed(bookId: string): Promise<DetailedBookTag[]> {
  const response = await fetch(`${API_BASE}/audiobooks/${bookId}/tags-detailed`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to get detailed tags');
  }
  const data = await response.json();
  return (data.tags || []) as DetailedBookTag[];
}

export async function bulkUpdateTags(
  bookIds: string[],
  addTags: string[],
  removeTags: string[]
): Promise<number> {
  const response = await fetch(`${API_BASE}/audiobooks/batch-tags`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      book_ids: bookIds,
      add_tags: addTags,
      remove_tags: removeTags,
    }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to bulk update tags');
  }
  const data = await response.json();
  return data.updated;
}

export async function listAllUserTags(): Promise<Array<{ tag: string; count: number }>> {
  const response = await fetch(`${API_BASE}/tags`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to list tags');
  }
  const data = await response.json();
  return data.data?.tags ?? data.tags ?? [];
}

// --- Column config preferences ---

export interface ColumnConfig {
  visibleColumns: string[]; // column IDs
  columnOrder: string[]; // column IDs in display order
  columnWidths: Record<string, number>; // column ID -> width in px
}

const COLUMN_CONFIG_KEY = 'library_column_config';

export async function getUserColumnConfig(): Promise<ColumnConfig | null> {
  const response = await fetch(`${API_BASE}/preferences/${COLUMN_CONFIG_KEY}`);
  if (!response.ok) return null;
  const data = await response.json();
  if (!data.value) return null;
  try {
    return JSON.parse(data.value) as ColumnConfig;
  } catch {
    return null;
  }
}

export async function saveUserColumnConfig(config: ColumnConfig): Promise<void> {
  const response = await fetch(`${API_BASE}/preferences/${COLUMN_CONFIG_KEY}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value: JSON.stringify(config) }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to save column config');
  }
}

export async function deleteUserColumnConfig(): Promise<void> {
  const response = await fetch(`${API_BASE}/preferences/${COLUMN_CONFIG_KEY}`, {
    method: 'DELETE',
  });
  // Ignore 404 — config may not exist
  if (!response.ok && response.status !== 404) {
    throw await buildApiError(response, 'Failed to delete column config');
  }
}

// --- Embedding-based deduplication ---

// T016: unified scoring band values (match internal/dedup/unified/score.go constants).
export type DedupBand = 'CERTAIN' | 'HIGH' | 'MEDIUM' | 'REVIEW';

// T016: single evidence signal stored in ScoreBreakdown.
export interface DedupSignal {
  kind: string; // e.g. "exact_file", "embedding_high", "duration"
  value: number; // raw signal value 0–1
  weight: number; // calibration weight
  evidence: string; // human-readable description for UI
  primary: boolean; // whether this signal alone can indicate a duplicate
}

// T016: composite score breakdown stored on each candidate.
export interface DedupScoreBreakdown {
  score: number; // 0–100 composite
  band: string; // CERTAIN | HIGH | MEDIUM | REVIEW
  signals: DedupSignal[];
  skipped_reason?: string;
  formula: string; // scoring algorithm version tag
}

export interface DedupCandidate {
  id: number;
  entity_type: 'book' | 'author';
  entity_a_id: string;
  entity_b_id: string;
  layer: 'exact' | 'embedding' | 'llm';
  similarity?: number;
  llm_verdict?: string;
  llm_reason?: string;
  status: 'pending' | 'merged' | 'dismissed';
  created_at: string;
  updated_at: string;
  // T016 extensions
  band?: DedupBand;
  formula_version?: string;
  score?: number;
  score_breakdown?: DedupScoreBreakdown;
  // Inline book enrichment, populated only when getDedupCandidates is called
  // with include_books=true. Lets the unified UI render rich cards
  // (title/author/path/metadata-quality) without per-book getBook() fetches.
  // null when the entity is a non-book type or the lookup missed.
  book_a?: Book | null;
  book_b?: Book | null;
}

export interface DedupCandidatesResponse {
  candidates: DedupCandidate[];
  total: number;
}

export interface DedupStats {
  entity_type: string;
  layer: string;
  status: string;
  count: number;
}

export async function getDedupCandidates(params?: {
  entity_type?: string;
  status?: string;
  layer?: string;
  min_similarity?: number;
  limit?: number;
  offset?: number;
  // T016 extensions
  band?: string;
  include_breakdown?: boolean;
  // When true, each candidate row carries inline book_a/book_b objects.
  include_books?: boolean;
  // When true, only pairs where NEITHER book has matched metadata are returned
  // (both low-quality, need manual matching).
  both_unmatched?: boolean;
}): Promise<DedupCandidatesResponse> {
  const qs = new URLSearchParams();
  if (params?.entity_type) qs.set('entity_type', params.entity_type);
  if (params?.status) qs.set('status', params.status);
  if (params?.layer) qs.set('layer', params.layer);
  if (params?.min_similarity != null) qs.set('min_similarity', String(params.min_similarity));
  if (params?.band) qs.set('band', params.band);
  if (params?.include_breakdown) qs.set('include_breakdown', 'true');
  if (params?.include_books) qs.set('include_books', 'true');
  if (params?.both_unmatched) qs.set('both_unmatched', 'true');
  if (params?.limit != null) qs.set('limit', String(params.limit));
  if (params?.offset != null) qs.set('offset', String(params.offset));
  const url = qs.toString() ? `${API_BASE}/dedup/candidates?${qs}` : `${API_BASE}/dedup/candidates`;
  const response = await fetch(url);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch dedup candidates');
  }
  const responseData = await response.json();
  return responseData.data;
}

export async function getDedupStats(): Promise<{ stats: DedupStats[] }> {
  const response = await fetch(`${API_BASE}/dedup/stats`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch dedup stats');
  }
  const responseData = await response.json();
  return responseData.data;
}

export async function mergeDedupCandidate(id: number, keepId?: string): Promise<void> {
  // When keepId is provided, the backend uses it as the merge primary
  // (which book is kept). When omitted, the backend falls back to
  // auto-select by format/bitrate/size. keepId must match the candidate's
  // entity_a_id or entity_b_id, otherwise the server returns 400.
  const init: RequestInit = { method: 'POST' };
  if (keepId) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify({ keep_id: keepId });
  }
  const response = await fetch(`${API_BASE}/dedup/candidates/${id}/merge`, init);
  // 409 = candidate was already merged/stale — the backend already marked it
  // merged and fired the event. Treat as success so the UI drops the row.
  if (response.status === 409) return;
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to merge dedup candidate');
  }
}

export async function dismissDedupCandidate(id: number): Promise<void> {
  const response = await fetch(`${API_BASE}/dedup/candidates/${id}/dismiss`, {
    method: 'POST',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to dismiss dedup candidate');
  }
}

export interface BulkMergeDedupResult {
  attempted: number;
  merged: number;
  failed: number;
  failures?: Array<{ candidate_id: number; reason: string }>;
}

export async function bulkMergeDedupCandidates(filter: {
  entity_type?: string;
  status?: string;
  layer?: string;
  min_similarity?: number;
  max_similarity?: number;
}): Promise<BulkMergeDedupResult> {
  const response = await fetch(`${API_BASE}/dedup/candidates/bulk-merge`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(filter),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to bulk-merge dedup candidates');
  }
  const responseData = await response.json();
  return responseData.data;
}

export interface ClusterMergeResult {
  status: string;
  merged_books: number;
  candidates_updated: number;
  result?: unknown;
}

// mergeDedupCluster merges a set of book IDs into one version group.
// If primaryBookId is provided, that book is forced as the version-group
// primary (overrides the bookIsBetter auto-pick based on path origin,
// curation, format, bitrate, size). If omitted, the backend auto-picks.
export async function mergeDedupCluster(
  bookIds: string[],
  primaryBookId?: string
): Promise<ClusterMergeResult> {
  const body: Record<string, unknown> = { book_ids: bookIds };
  if (primaryBookId) {
    body.primary_book_id = primaryBookId;
  }
  const response = await fetch(`${API_BASE}/dedup/candidates/merge-cluster`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to merge dedup cluster');
  }
  const responseData = await response.json();
  return responseData.data;
}

export interface DedupSeriesSummary {
  series_id: number;
  series_name: string;
  cluster_count: number;
  book_count: number;
  candidate_count: number;
}

export async function listDedupCandidateSeries(): Promise<DedupSeriesSummary[]> {
  const response = await fetch(`${API_BASE}/dedup/candidates/series-summary`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to list dedup series summary');
  }
  const envelope = await response.json();
  return envelope.data.series || [];
}

export interface SeriesMergeResult {
  series_id: number;
  clusters_merged: number;
  books_merged: number;
  candidates_updated: number;
  failures?: string[];
}

export async function mergeDedupCandidateSeries(seriesId: number): Promise<SeriesMergeResult> {
  const response = await fetch(`${API_BASE}/dedup/candidates/merge-series`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ series_id: seriesId }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to merge dedup series');
  }
  const responseData = await response.json();
  return responseData.data;
}

export async function dismissDedupCluster(
  bookIds: string[]
): Promise<{ status: string; dismissed: number }> {
  const response = await fetch(`${API_BASE}/dedup/candidates/dismiss-cluster`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ book_ids: bookIds }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to dismiss dedup cluster');
  }
  const responseData = await response.json();
  return responseData.data;
}

// Remove one or more books from a cluster by dismissing only the
// edges between them and the remaining cluster members. Pairs
// involving a removed book with books OUTSIDE the cluster are left
// alone. Accepts either a single book ID (× button) or a list of
// IDs (multi-select split).
export async function removeFromDedupCluster(
  clusterBookIds: string[],
  removeBookIds: string | string[]
): Promise<{ status: string; dismissed: number; removed: number }> {
  const body: Record<string, unknown> = {
    cluster_book_ids: clusterBookIds,
  };
  if (Array.isArray(removeBookIds)) {
    body.remove_book_ids = removeBookIds;
  } else {
    body.remove_book_id = removeBookIds;
  }
  const response = await fetch(`${API_BASE}/dedup/candidates/remove-from-cluster`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to remove book from dedup cluster');
  }
  const responseData = await response.json();
  return responseData.data;
}

// --- Split-book backfill (MAYDEPLOY-G2/G3/G4) ---

export interface SplitBookCandidate {
  id: string;
  parent_folder: string;
  book_ids: string[];
  suggested_title: string;
  suggested_author: string;
  sequential_pattern: string;
  shape: 'parent' | 'grandparent' | string;
}

export interface SplitBookCandidatesResponse {
  candidates: SplitBookCandidate[];
  total: number;
}

export interface SplitBookMergeResult {
  keep_id?: string;
  merged_files?: number;
  merged_sources?: number;
  [key: string]: unknown;
}

// Trigger the split-book backfill scan (dedup.split-book-scan op).
// Backend returns {op_id: "..."} — normalized to an Operation-shaped
// object so the caller can drop it into useOperationsStore.startPolling.
export async function triggerSplitBookScan(): Promise<Operation> {
  return wrapTrigger('dedup.split-book-scan', async () => {
    const response = await fetch(`${API_BASE}/dedup/split-book-scan`, {
      method: 'POST',
    });
    if (!response.ok) {
      throw await buildApiError(response, 'Failed to trigger split-book scan');
    }
    const responseData = await response.json();
    const raw = (responseData.data ?? {}) as Record<string, unknown>;
    const id = (raw.id ?? raw.op_id ?? '') as string;
    return { ...raw, id, type: (raw.type as string) ?? 'dedup.split-book-scan' } as Operation;
  });
}

// Fetch all persisted split-book candidates. The backend does NOT paginate
// — clusters are small (hundreds at most) — so callers paginate client-side.
export async function getSplitBookCandidates(): Promise<SplitBookCandidatesResponse> {
  const response = await fetch(`${API_BASE}/dedup/split-book-candidates`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch split-book candidates');
  }
  const responseData = await response.json();
  return responseData.data;
}

// Merge a single split-book candidate cluster. If keepId is omitted the
// backend uses the suggested keep (BookIDs[0], earliest ULID). On success
// the candidate row is deleted server-side.
export async function mergeSplitBookCandidate(
  id: string,
  keepId?: string
): Promise<SplitBookMergeResult> {
  const init: RequestInit = { method: 'POST' };
  if (keepId) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify({ keep_id: keepId });
  }
  const response = await fetch(
    `${API_BASE}/dedup/split-book-candidates/${encodeURIComponent(id)}/merge`,
    init
  );
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to merge split-book candidate');
  }
  const responseData = await response.json();
  return responseData.data;
}

// All trigger* dedup endpoints return the full Operation row (id, type,
// status, progress, ...). Returning the bare Operation lets callers
// register the op with useOperationsStore.startPolling immediately so the
// bell icon and Activity page surface the op without waiting for the next
// 15s background sweep — that wait is what made fast scans look invisible
// after the user clicked "Re-scan" or "Re-embed All".
export async function triggerDedupScan(): Promise<Operation> {
  return wrapTrigger('dedup.scan', async () => {
    const response = await fetch(`${API_BASE}/dedup/scan`, { method: 'POST' });
    if (!response.ok) throw await buildApiError(response, 'Failed to trigger dedup scan');
    return (await response.json()).data;
  });
}

export async function triggerDedupLLM(): Promise<Operation> {
  return wrapTrigger('dedup.scan-llm', async () => {
    const response = await fetch(`${API_BASE}/dedup/scan-llm`, { method: 'POST' });
    if (!response.ok) throw await buildApiError(response, 'Failed to trigger dedup LLM scan');
    return (await response.json()).data;
  });
}

export async function triggerDedupAcoustID(): Promise<Operation> {
  return wrapTrigger('acoustid.scan', async () => {
    const response = await fetch(`${API_BASE}/dedup/scan-acoustid`, { method: 'POST' });
    if (!response.ok) throw await buildApiError(response, 'Failed to trigger AcoustID scan');
    const raw = (await response.json()).data ?? {};
    return { ...raw, id: raw.id ?? raw.op_id ?? '' } as Operation;
  });
}

// Triggers the acoustid.lookup-online op via the generic /operations/v2
// endpoint. Sends every BookFile's whole-file chromaprint to
// acoustid.org's /v2/lookup and stores the top MusicBrainz recording
// match (score ≥ 0.85). Requires the server-side ACOUSTID_API_KEY env
// var; otherwise the op fails fast with a clear message.
export async function triggerAcoustIDOnlineLookup(force = false): Promise<Operation> {
  return wrapTrigger('acoustid.lookup-online', async () => {
    const response = await fetch(`${API_BASE}/operations/v2`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ def_id: 'acoustid.lookup-online', params: { force } }),
    });
    if (!response.ok)
      throw await buildApiError(response, 'Failed to trigger AcoustID online lookup');
    const raw = await response.json();
    const opID = raw.op_id ?? raw.data?.op_id ?? raw.data?.id ?? '';
    return { id: opID, type: 'acoustid.lookup-online' } as Operation;
  });
}

export async function triggerFingerprintBackfill(
  scope: 'missing' | 'all' = 'missing'
): Promise<Operation> {
  return wrapTrigger('acoustid.fingerprint-rescan', async () => {
    const response = await fetch(`${API_BASE}/dedup/fingerprint-rescan`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ scope }),
    });
    if (!response.ok) throw await buildApiError(response, 'Failed to trigger fingerprint backfill');
    const raw = (await response.json()).data ?? {};
    return { ...raw, id: raw.id ?? raw.op_id ?? '' } as Operation;
  });
}

export interface AcoustIDSegmentComparison {
  segment: string;
  hash_a: string;
  hash_b: string;
  match: boolean;
}

export interface AcoustIDCompareResponse {
  book_a: Book;
  book_b: Book;
  overall_score: number;
  segment_scores: AcoustIDSegmentComparison[];
}

export async function compareAcoustID(
  bookAID: string,
  bookBID: string
): Promise<AcoustIDCompareResponse> {
  const response = await fetch(
    `${API_BASE}/audiobooks/${encodeURIComponent(bookAID)}/compare-acoustid?other=${encodeURIComponent(bookBID)}`,
    { method: 'POST' }
  );
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to compare AcoustID fingerprints');
  }
  const responseData = await response.json();
  return responseData.data ?? responseData;
}

export async function triggerDedupRefresh(): Promise<Operation> {
  return wrapTrigger('dedup.refresh', async () => {
    const response = await fetch(`${API_BASE}/dedup/refresh`, { method: 'POST' });
    if (!response.ok) throw await buildApiError(response, 'Failed to trigger dedup refresh');
    return (await response.json()).data;
  });
}

// Enqueues the dedup.purge-stale UOS op so the cleanup appears in the bell
// just like every other user-triggered backend action. Returns the op id;
// the actual deleted count lands in the op's final progress message which
// the bell reads.
export async function purgeStaleCandidates(): Promise<{ op_id: string }> {
  return wrapTrigger('dedup.purge-stale', async () => {
    const response = await fetch(`${API_BASE}/dedup/purge-stale`, { method: 'POST' });
    if (!response.ok) throw await buildApiError(response, 'Failed to purge stale candidates');
    const data = (await response.json()).data ?? {};
    return { op_id: data.op_id ?? '', id: data.op_id ?? '' } as { op_id: string; id: string };
  });
}

export async function triggerEmbedScan(): Promise<Operation> {
  return wrapTrigger('dedup.embed', async () => {
    const response = await fetch(`${API_BASE}/dedup/embed`, { method: 'POST' });
    if (!response.ok) throw await buildApiError(response, 'Failed to trigger embedding scan');
    return (await response.json()).data;
  });
}

// Enqueues acoustid.reset-all followed by a forced acoustid.fingerprint-rescan.
// Both ops share the "acoustid.fingerprint" concurrency key so the rescan
// queues behind the reset and runs sequentially.
export async function resetAcoustIDFingerprints(): Promise<{
  reset_op_id: string;
  rescan_op_id: string;
}> {
  return wrapTrigger('acoustid.reset-all', async () => {
    const response = await fetch(`${API_BASE}/dedup/reset-acoustid`, { method: 'POST' });
    if (!response.ok) throw await buildApiError(response, 'Failed to reset AcoustID fingerprints');
    const data = (await response.json()).data ?? {};
    return {
      reset_op_id: data.reset_op_id ?? data.op_id ?? '',
      rescan_op_id: data.rescan_op_id ?? '',
      op_id: data.reset_op_id ?? data.op_id ?? '',
      id: data.reset_op_id ?? data.op_id ?? '',
    } as { reset_op_id: string; rescan_op_id: string; op_id: string; id: string };
  });
}

// ── API Key management ────────────────────────────────────────────────────────

export interface APIKey {
  id: string;
  user_id: string;
  name: string;
  description: string;
  scopes: string[];
  status: 'active' | 'inactive' | 'revoked';
  created_at: string;
  last_used_at?: string;
  last_used_ip?: string;
  use_count: number;
  expires_at?: string;
  deactivated_at?: string;
  revoked_at?: string;
  identifier: string;
  days_since_last_use: number | null;
  never_used: boolean;
  username?: string;
}

export interface CreateAPIKeyRequest {
  name: string;
  description?: string;
  scopes?: string[];
  expires_in_days?: number;
  user_id?: string;
}

export interface CreateAPIKeyResponse {
  id: string;
  name: string;
  token: string;
  scopes: string[];
  expires_at?: string;
  created_at: string;
}

export async function createAPIKey(body: CreateAPIKeyRequest): Promise<CreateAPIKeyResponse> {
  const response = await fetch(`${API_BASE}/auth/api-keys`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to create API key');
  }
  const body_data = await response.json();
  return body_data.data;
}

export async function listAPIKeys(all?: boolean): Promise<APIKey[]> {
  const url = all ? `${API_BASE}/auth/api-keys?all=true` : `${API_BASE}/auth/api-keys`;
  const response = await fetch(url);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to list API keys');
  }
  const body_data = await response.json();
  return body_data.data?.api_keys ?? [];
}

export async function getAPIKey(id: string): Promise<APIKey> {
  const response = await fetch(`${API_BASE}/auth/api-keys/${id}`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to get API key');
  }
  const body_data = await response.json();
  return body_data.data;
}

export async function updateAPIKeyStatus(
  id: string,
  status: 'active' | 'inactive'
): Promise<APIKey> {
  const response = await fetch(`${API_BASE}/auth/api-keys/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ status }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to update API key status');
  }
  const body_data = await response.json();
  return body_data.data;
}

export async function revokeAPIKey(id: string): Promise<void> {
  const response = await fetch(`${API_BASE}/auth/api-keys/${id}`, {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to revoke API key');
  }
}

// Cache stats types and functions
export interface CacheStatsDuration {
  count: number;
  sum: number;
}

// Go nil maps serialize as JSON null; use Record | null to match the server shape.
export type CacheMisses = Record<string, number> | null;
export type CacheInvalidations = Record<string, number> | null;
export type CacheEvictions = Record<string, number> | null;

export interface CacheStatsEntry {
  name: string;
  hits: number;
  misses: CacheMisses;
  sets: number;
  invalidations: CacheInvalidations;
  evictions: CacheEvictions;
  size: number;
  hit_rate?: number; // null when no misses
  get_duration_seconds: CacheStatsDuration;
}

export interface CacheStatsResponse {
  caches: CacheStatsEntry[];
  generated_at: string;
}

export async function getCacheStats(): Promise<CacheStatsResponse> {
  const response = await fetch(`${API_BASE}/cache/stats`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch cache stats');
  }
  const body_data = await response.json();
  return body_data.data ?? body_data;
}

// ─── Chapter Consolidation (MATCH-2) ─────────────────────────────────────────

export interface ChapterGroup {
  primary_book_id: string;
  book_ids: string[];
  common_title: string;
  total_duration: number;
  file_count: number;
}

export interface ChapterGroupsResult {
  groups: ChapterGroup[];
  total_books_affected: number;
}

export interface ChapterMergeResult {
  dry_run: boolean;
  groups_found: number;
  books_merged: number;
  books_skipped: number;
  groups: ChapterGroup[];
}

export async function scanChapterGroups(params?: {
  min_files?: number;
  max_per_file_duration?: number;
  path_prefix?: string;
}): Promise<ChapterGroupsResult> {
  const q = new URLSearchParams();
  if (params?.min_files != null) q.set('min_files', String(params.min_files));
  if (params?.max_per_file_duration != null)
    q.set('max_per_file_duration', String(params.max_per_file_duration));
  if (params?.path_prefix) q.set('path_prefix', params.path_prefix);
  const url = `${API_BASE}/maintenance/chapter-groups${q.toString() ? `?${q}` : ''}`;
  const response = await fetch(url);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to scan chapter groups');
  }
  const body_data = await response.json();
  return body_data.data ?? body_data;
}

export async function mergeChapterGroups(params?: {
  dry_run?: boolean;
  min_files?: number;
  max_per_file_duration?: number;
}): Promise<ChapterMergeResult> {
  const q = new URLSearchParams();
  if (params?.dry_run != null) q.set('dry_run', String(params.dry_run));
  if (params?.min_files != null) q.set('min_files', String(params.min_files));
  if (params?.max_per_file_duration != null)
    q.set('max_per_file_duration', String(params.max_per_file_duration));
  const url = `${API_BASE}/maintenance/merge-chapter-groups${q.toString() ? `?${q}` : ''}`;
  const response = await fetch(url, { method: 'POST' });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to merge chapter groups');
  }
  const body_data = await response.json();
  return body_data.data ?? body_data;
}

// ─── SHA Duplicate File Detection (FILE-SHA-2) ───────────────────────────────

export interface DuplicateFileInfo {
  book_file_id: string;
  book_id: string;
  book_title: string;
  file_path: string;
  book_path: string;
  file_size_bytes: number;
}

export interface DuplicateFileGroup {
  hash: string;
  file_count: number;
  book_count: number;
  total_size_bytes: number;
  files: DuplicateFileInfo[];
}

export interface DuplicateFilesResult {
  groups: DuplicateFileGroup[];
  total_wasted_bytes: number;
  total_groups: number;
}

export async function scanDuplicateFiles(limit?: number): Promise<DuplicateFilesResult> {
  const q = new URLSearchParams();
  if (limit != null) q.set('limit', String(limit));
  const url = `${API_BASE}/maintenance/duplicate-files${q.toString() ? `?${q}` : ''}`;
  const response = await fetch(url);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to scan duplicate files');
  }
  const body_data = await response.json();
  const result = body_data.data ?? body_data;
  // Ensure groups is always an array even if the server returns null.
  result.groups = result.groups ?? [];
  return result;
}

// ── MATCH-4: metadata-hash duplicate scan ─────────────────────────────────────

export interface MetadataHashDupBook {
  id: string;
  title: string;
  file_count: number;
}

export interface MetadataHashDupGroup {
  hash: string;
  books: MetadataHashDupBook[];
}

export interface MetadataHashDuplicatesResult {
  groups: MetadataHashDupGroup[];
  total_duplicate_books: number;
}

export async function findMetadataHashDuplicates(): Promise<MetadataHashDuplicatesResult> {
  const response = await fetch(`${API_BASE}/maintenance/metadata-hash-duplicates`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to scan metadata-hash duplicates');
  }
  return response.json();
}

// ── Hash coverage stats + backfill ────────────────────────────────────────────

export interface BookFileHashStatsByLib {
  path: string;
  total_files: number;
  with_hash: number;
  missing_hash: number;
}

export interface BookFileHashStats {
  total_book_files: number;
  with_file_hash: number;
  missing_file_hash: number;
  with_original_hash: number;
  total_books: number;
  books_with_no_files: number;
  by_library: BookFileHashStatsByLib[];
}

export async function getBookFileHashStats(): Promise<BookFileHashStats> {
  const response = await fetch(`${API_BASE}/maintenance/book-file-hash-stats`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch hash stats');
  }
  const body = await response.json();
  return body.data ?? body;
}

export interface BackfillHashesResult {
  dry_run: boolean;
  updated: number;
  skipped: number;
  failed: number;
}

export async function backfillFileHashes(dryRun = false): Promise<{ operation_id: string }> {
  const response = await fetch(
    `${API_BASE}/maintenance/jobs/${encodeURIComponent('backfill-file-hashes')}`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ dry_run: dryRun }),
    }
  );
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to start backfill file hashes job');
  }
  return response.json();
}

// New: metadata hash stats + backfill
export interface BookMetadataHashStats {
  total_books: number;
  with_metadata_hash: number;
  missing_metadata_hash: number;
  with_asin_or_isbn: number;
  missing_hash_has_id: number;
  by_library: BookMetadataHashStatsByLib[];
}

export interface BookMetadataHashStatsByLib {
  path: string;
  total_books: number;
  with_metadata_hash: number;
  missing_metadata_hash: number;
  missing_hash_has_id: number;
}

export async function getBookMetadataHashStats(): Promise<BookMetadataHashStats> {
  const response = await fetch(`${API_BASE}/maintenance/book-metadata-hash-stats`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch metadata hash stats');
  }
  const body = await response.json();
  return body.data ?? body;
}

export interface AcoustIDStats {
  total_files: number;
  with_fingerprint: number;
  by_library: { library_root: string; total_files: number; with_fingerprint: number }[];
}

export async function getAcoustIDStats(): Promise<AcoustIDStats> {
  const response = await fetch(`${API_BASE}/maintenance/acoustid-stats`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch AcoustID stats');
  }
  const body = await response.json();
  return body.data?.data ?? body.data ?? body;
}

export async function backfillMetadataHashes(dryRun = false): Promise<{ operation_id: string }> {
  const response = await fetch(
    `${API_BASE}/maintenance/jobs/${encodeURIComponent('backfill-metadata-source-hash')}`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ dry_run: dryRun }),
    }
  );
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to start backfill metadata job');
  }
  return response.json();
}

// ── Unified Maintenance Jobs ──────────────────────────────────────────────────

export interface MaintenanceJobDef {
  id: string;
  description: string;
  can_resume: boolean;
}

export interface MaintenanceJobsResult {
  jobs: MaintenanceJobDef[];
}

export async function listMaintenanceJobs(): Promise<MaintenanceJobDef[]> {
  const response = await fetch(`${API_BASE}/maintenance/jobs`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to list maintenance jobs');
  }
  const body = await response.json();
  return (body as MaintenanceJobsResult).jobs ?? [];
}

export async function runMaintenanceJob(
  jobId: string,
  dryRun = false
): Promise<{ operation_id: string }> {
  const response = await fetch(`${API_BASE}/maintenance/jobs/${encodeURIComponent(jobId)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ dry_run: dryRun }),
  });
  if (!response.ok) {
    throw await buildApiError(response, `Failed to run maintenance job "${jobId}"`);
  }
  return response.json();
}

export async function startOptimize(): Promise<{ operation_id: string }> {
  return wrapTrigger('library.optimize', async () => {
    const response = await fetch(`${API_BASE}/operations/optimize`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    });
    if (!response.ok) throw await buildApiError(response, 'Failed to start optimize operation');
    return response.json();
  });
}

// QuickQuery matches the QuickQuery interface defined in web/src/types/index.ts.
export interface QuickQueryItem {
  id: string;
  label: string;
  count: number;
  filter: Record<string, unknown>;
}

/** Fetches the six preset quick-filter counts for the Library header kebab menu. */
export async function getQuickQueries(): Promise<QuickQueryItem[]> {
  const response = await fetch(`${API_BASE}/library/quick-queries`);
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to load quick queries');
  }
  const data = (await response.json()) as { queries: QuickQueryItem[] };
  return data.queries ?? [];
}

// --- T016: Unified Dedup API extensions ---

// BookDetail returned by the breakdown endpoint (book + files).
export interface DedupBookDetail {
  id: string;
  title: string;
  author_id?: string | null;
  author_name?: string;
  series_id?: number | null;
  series_name?: string;
  format?: string;
  duration?: number;
  file_path?: string;
  cover_url?: string;
  files: Array<{
    id: string;
    file_path: string;
    format?: string;
    bitrate?: number;
    file_size?: number;
    duration?: number;
  }>;
}

// Response shape for GET /api/v1/dedup/candidates/:id/breakdown.
export interface DedupCandidateBreakdownResponse {
  candidate: DedupCandidate;
  book_a: DedupBookDetail | null;
  book_b: DedupBookDetail | null;
}

// Fetch a single candidate with full score breakdown + both books' details.
// Used by CandidateCompareDrawer to render the side-by-side comparison.
export async function getDedupCandidateBreakdown(
  id: number,
  signal?: AbortSignal
): Promise<DedupCandidateBreakdownResponse> {
  const response = await fetch(`${API_BASE}/dedup/candidates/${id}/breakdown`, { signal });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch candidate breakdown');
  }
  const responseData = await response.json();
  return responseData.data;
}

// Response shape for POST /api/v1/dedup/rescore.
export interface DedupRescoreResult {
  inspected: number;
  skipped: number;
  changed: number;
  applied: boolean;
  band_deltas: Record<string, number>; // e.g. {"HIGH→CERTAIN": 3}
}

// Re-run unified.ComposeScore over stored signal sets of all pending candidates.
// Pass apply=false for a dry-run (returns counts without persisting).
export async function rescoreDedupCandidates(apply = false): Promise<DedupRescoreResult> {
  const response = await fetch(`${API_BASE}/dedup/rescore`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ apply }),
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to rescore dedup candidates');
  }
  const responseData = await response.json();
  return responseData.data;
}

// --- Tool lifecycle ---

export interface ToolStatus {
  name: string;
  mode: 'managed' | 'system' | 'custom' | 'disabled';
  available: boolean;
  resolved_path?: string;
  version?: string;
}

export async function getTools(): Promise<ToolStatus[]> {
  const response = await fetch(`${API_BASE}/tools`, {
    credentials: 'include',
  });
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to fetch tools');
  }
  return response.json();
}

export async function installTool(name: string): Promise<{ path: string; version: string }> {
  const response = await fetch(`${API_BASE}/tools/${name}/install`, {
    method: 'POST',
    credentials: 'include',
  });
  if (!response.ok) {
    throw await buildApiError(response, `Failed to install ${name}`);
  }
  return response.json();
}

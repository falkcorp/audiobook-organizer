// file: web/src/types/index.ts
// version: 1.18.0
// guid: 0d1e2f3a-4b5c-6d7e-8f9a-0b1c2d3e4f5a
// last-edited: 2026-07-11

// Audiobook (Book) type
export interface Audiobook {
  id: string;
  title: string;
  author?: string;
  narrator?: string;
  series?: string;
  series_number?: number;
  genre?: string;
  year?: number;
  print_year?: number;
  audiobook_release_year?: number;
  language?: string;
  publisher?: string;
  edition?: string;
  isbn10?: string;
  isbn13?: string;
  description?: string;
  track_number?: string;
  total_tracks?: number;
  disk_number?: string;
  total_disks?: number;
  duration_seconds?: number;
  file_path: string;
  original_filename?: string;
  cover_path?: string;
  cover_url?: string;
  itunes_path?: string;
  narrators_json?: string;
  file_size_bytes?: number;
  format?: string;
  bitrate_kbps?: number;

  // Media info fields (parsed from file)
  codec?: string;
  sample_rate_hz?: number;
  channels?: number;
  bit_depth?: number;
  quality?: string; // e.g., '320kbps AAC', '128kbps MP3', 'FLAC Lossless'

  // Version management
  is_primary_version?: boolean;
  version_group_id?: string; // Links multiple versions of same book
  version_notes?: string; // e.g., 'Remastered 2020', 'Original Recording'
  file_hash?: string;
  original_file_hash?: string;
  organized_file_hash?: string;
  library_state?: string;
  quantity?: number;
  marked_for_deletion?: boolean;
  marked_for_deletion_at?: string;
  quarantine_reason?: string;
  quarantined_at?: string;
  organize_error?: string;
  metadata_review_status?: string; // null, "matched", "no_match"
  metadata_updated_at?: string;
  last_written_at?: string;

  // User ratings (RATE-1/RATE-2)
  user_rating_overall?: number | null;
  user_rating_story?: number | null;
  user_rating_performance?: number | null;
  user_rating_notes?: string | null;

  // Fingerprinting fields
  fingerprint_status?: "none" | "partial" | "complete";
  fingerprinted_file_count?: number;
  total_file_count?: number;
  coverage_percent?: number;
  last_fingerprinted_at?: string; // ISO timestamp

  created_at: string;
  updated_at: string;
  work_id?: string;
  tags?: string[];
}

// Alias for backend compatibility
export type Book = Audiobook;

// Work type
export interface Work {
  id: string;
  title: string;
  author?: string;
  series?: string;
  series_position?: number;
  description?: string;
  original_language?: string;
  first_published_year?: number;
  created_at: string;
  updated_at: string;
}

// Author type
export interface Author {
  id: string;
  name: string;
  biography?: string;
  birth_year?: number;
  death_year?: number;
  created_at: string;
  updated_at: string;
}

// Series type
export interface Series {
  id: string;
  title: string;
  description?: string;
  total_books?: number;
  created_at: string;
  updated_at: string;
}

// User type
export interface User {
  id: string;
  name: string;
  email: string;
}

// API Error type
export interface ApiError {
  message: string;
  code?: string;
  details?: Record<string, unknown>;
}

// Sort field enum for type safety
export enum SortField {
  Title = 'title',
  Author = 'author',
  Year = 'year',
  Series = 'series',
  CreatedAt = 'created_at',
  Narrator = 'narrator',
  Genre = 'genre',
  Language = 'language',
  Publisher = 'publisher',
  Format = 'format',
  Duration = 'duration_seconds',
  Bitrate = 'bitrate_kbps',
  Codec = 'codec',
  FileSize = 'file_size_bytes',
  UpdatedAt = 'updated_at',
  LibraryState = 'library_state',
}

// Sort order enum for type safety
export enum SortOrder {
  Ascending = 'asc',
  Descending = 'desc',
}

// Filter options for audiobook queries
export interface FilterOptions {
  search?: string;
  author?: string;
  series?: string;
  genre?: string;
  language?: string;
  libraryState?: string;
  tags?: string[];
  sortBy?: SortField;
  sortOrder?: SortOrder;
  showFailed?: boolean;
  hasFileErrors?: boolean;
  fingerprintStatus?: "complete" | "partial" | "none";
  coveragePercentMin?: number;
  coveragePercentMax?: number;
  // Quick-filter presets (from header kebab menu)
  missingCovers?: boolean;
  inImportPath?: boolean;
  noIsbn?: boolean;
  duplicatesFlagged?: boolean;
}

// QuickQuery is one entry returned by GET /api/v1/library/quick-queries.
export interface QuickQuery {
  id: string;
  label: string;
  count: number;
  filter: Partial<FilterOptions>;
}

// Pagination parameters
export interface PaginationParams {
  page: number;
  limit: number;
}

// Paginated response wrapper
export interface PaginatedResponse<T> {
  data: T[];
  total: number;
  page: number;
  limit: number;
  has_more: boolean;
}

// BookFile type (file-level representation with fingerprinting fields)
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
  download_hash?: string;
  original_file_hash?: string;
  post_metadata_hash?: string;
  // Acoustic fingerprint segments (0=intro, 1-5=body, 6=outro)
  acoustid_seg0?: string;
  acoustid_seg1?: string;
  acoustid_seg2?: string;
  acoustid_seg3?: string;
  acoustid_seg4?: string;
  acoustid_seg5?: string;
  acoustid_seg6?: string;
  fingerprint_failed_at?: string | null;
  fingerprint_failure_reason?: string | null;
  fingerprint_failure_detail?: string | null;
  fingerprint_diagnostic_json?: string | null;
  organize_method?: string;
  skip_scan?: boolean;
  missing: boolean;
  created_at: string;
  updated_at: string;
}

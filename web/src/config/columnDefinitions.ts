// file: web/src/config/columnDefinitions.ts
// version: 1.3.0
// guid: a7b8c9d0-e1f2-4a3b-5c6d-7e8f9a0b1c2d

import { Audiobook } from '../types';

// --- Formatters ---

export function formatDuration(seconds: unknown): string {
  if (seconds == null || typeof seconds !== 'number' || isNaN(seconds)) return '';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

export function formatFileSize(bytes: unknown): string {
  if (bytes == null || typeof bytes !== 'number' || isNaN(bytes)) return '';
  if (bytes >= 1_073_741_824) return `${(bytes / 1_073_741_824).toFixed(2)} GB`;
  if (bytes >= 1_048_576) return `${(bytes / 1_048_576).toFixed(2)} MB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${bytes} B`;
}

export function formatDate(value: unknown): string {
  if (value == null || typeof value !== 'string' || value === '') return '';
  try {
    return new Date(value).toLocaleDateString();
  } catch {
    return String(value);
  }
}

export function formatBoolean(value: unknown): string {
  if (value == null) return '';
  return value ? 'Yes' : 'No';
}

export function formatNumber(value: unknown): string {
  if (value == null) return '';
  return String(value);
}

export function formatRelativeDate(value: unknown): string {
  if (value == null || typeof value !== 'string' || value === '') return '—';
  try {
    const date = new Date(value);
    const now = Date.now();
    const diffMs = date.getTime() - now;
    const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));

    if (diffDays === 0) return 'today';
    if (diffDays === -1) return 'yesterday';
    if (diffDays < 0 && diffDays > -7) return `${Math.abs(diffDays)}d ago`;
    if (diffDays < 0 && diffDays > -30) return `${Math.abs(Math.floor(diffDays / 7))}w ago`;
    if (diffDays < 0) return `${Math.abs(Math.floor(diffDays / 30))}mo ago`;

    return new Date(value).toLocaleDateString();
  } catch {
    return String(value);
  }
}

// --- Column Definition ---

export interface ColumnDefinition {
  id: string;
  label: string;
  category: string;
  accessor: (book: Audiobook) => string | number | boolean | undefined | null;
  formatter?: (value: unknown) => string;
  sortKey: string;
  searchKey: string;
  defaultWidth: number;
  minWidth: number;
  sortable: boolean;
  defaultVisible: boolean;
}

// --- Categories ---

export const COLUMN_CATEGORIES = ['Basic', 'Media', 'iTunes', 'Lifecycle', 'Fingerprinting', 'IDs'] as const;
export type ColumnCategory = (typeof COLUMN_CATEGORIES)[number];

// --- Column Definitions ---

export const ALL_COLUMNS: ColumnDefinition[] = [
  // ---- Basic ----
  {
    id: 'title',
    label: 'Title',
    category: 'Basic',
    accessor: (b) => b.title,
    sortKey: 'title',
    searchKey: 'title',
    defaultWidth: 280,
    minWidth: 120,
    sortable: true,
    defaultVisible: true,
  },
  {
    id: 'author',
    label: 'Author',
    category: 'Basic',
    accessor: (b) => b.author,
    sortKey: 'author',
    searchKey: 'author',
    defaultWidth: 180,
    minWidth: 100,
    sortable: true,
    defaultVisible: true,
  },
  {
    id: 'narrator',
    label: 'Narrator',
    category: 'Basic',
    accessor: (b) => b.narrator,
    sortKey: 'narrator',
    searchKey: 'narrator',
    defaultWidth: 180,
    minWidth: 100,
    sortable: true,
    defaultVisible: true,
  },
  {
    id: 'series',
    label: 'Series',
    category: 'Basic',
    accessor: (b) => b.series,
    sortKey: 'series',
    searchKey: 'series',
    defaultWidth: 180,
    minWidth: 100,
    sortable: true,
    defaultVisible: true,
  },
  {
    id: 'series_number',
    label: 'Series #',
    category: 'Basic',
    accessor: (b) => b.series_number,
    formatter: formatNumber,
    // series_position: no server comparator has ever known series_number, so
    // this column's header sort ordered nothing until it pointed here.
    sortKey: 'series_position',
    searchKey: 'series_number',
    defaultWidth: 80,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'genre',
    label: 'Genre',
    category: 'Basic',
    accessor: (b) => b.genre,
    sortKey: 'genre',
    searchKey: 'genre',
    defaultWidth: 140,
    minWidth: 80,
    sortable: true,
    defaultVisible: true,
  },
  {
    id: 'year',
    label: 'Year',
    category: 'Basic',
    accessor: (b) => b.year,
    formatter: formatNumber,
    sortKey: 'year',
    searchKey: 'year',
    defaultWidth: 70,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'print_year',
    label: 'Print Year',
    category: 'Basic',
    accessor: (b) => b.print_year,
    formatter: formatNumber,
    sortKey: 'print_year',
    searchKey: 'print_year',
    defaultWidth: 90,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'audiobook_release_year',
    label: 'Release Year',
    category: 'Basic',
    accessor: (b) => b.audiobook_release_year,
    formatter: formatNumber,
    sortKey: 'audiobook_release_year',
    searchKey: 'release_year',
    defaultWidth: 100,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'language',
    label: 'Language',
    category: 'Basic',
    accessor: (b) => b.language,
    sortKey: 'language',
    searchKey: 'language',
    defaultWidth: 100,
    minWidth: 70,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'publisher',
    label: 'Publisher',
    category: 'Basic',
    accessor: (b) => b.publisher,
    sortKey: 'publisher',
    searchKey: 'publisher',
    defaultWidth: 160,
    minWidth: 100,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'edition',
    label: 'Edition',
    category: 'Basic',
    accessor: (b) => b.edition,
    sortKey: 'edition',
    searchKey: 'edition',
    defaultWidth: 120,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'description',
    label: 'Description',
    category: 'Basic',
    accessor: (b) => b.description,
    sortKey: 'description',
    searchKey: 'description',
    defaultWidth: 300,
    minWidth: 120,
    sortable: false,
    defaultVisible: false,
  },
  {
    id: 'tags',
    label: 'Tags',
    category: 'Basic',
    accessor: (b) => b.tags?.join(', ') ?? '',
    sortKey: 'tags',
    searchKey: 'tag',
    defaultWidth: 200,
    minWidth: 80,
    sortable: false,
    defaultVisible: false,
  },

  // ---- Media ----
  {
    id: 'format',
    label: 'Format',
    category: 'Media',
    accessor: (b) => b.format,
    sortKey: 'format',
    searchKey: 'format',
    defaultWidth: 80,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'duration_seconds',
    label: 'Duration',
    category: 'Media',
    accessor: (b) => b.duration_seconds,
    formatter: formatDuration,
    sortKey: 'duration_seconds',
    searchKey: 'duration',
    defaultWidth: 100,
    minWidth: 70,
    sortable: true,
    defaultVisible: true,
  },
  {
    id: 'file_size_bytes',
    label: 'File Size',
    category: 'Media',
    accessor: (b) => b.file_size_bytes,
    formatter: formatFileSize,
    sortKey: 'file_size_bytes',
    searchKey: 'file_size',
    defaultWidth: 100,
    minWidth: 70,
    sortable: true,
    defaultVisible: true,
  },
  {
    id: 'bitrate_kbps',
    label: 'Bitrate',
    category: 'Media',
    accessor: (b) => b.bitrate_kbps,
    formatter: (v) => (v != null && typeof v === 'number' ? `${v} kbps` : ''),
    sortKey: 'bitrate_kbps',
    searchKey: 'bitrate',
    defaultWidth: 100,
    minWidth: 70,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'codec',
    label: 'Codec',
    category: 'Media',
    accessor: (b) => b.codec,
    sortKey: 'codec',
    searchKey: 'codec',
    defaultWidth: 90,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'sample_rate_hz',
    label: 'Sample Rate',
    category: 'Media',
    accessor: (b) => b.sample_rate_hz,
    formatter: (v) => (v != null && typeof v === 'number' ? `${(v / 1000).toFixed(1)} kHz` : ''),
    sortKey: 'sample_rate_hz',
    searchKey: 'sample_rate',
    defaultWidth: 110,
    minWidth: 70,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'channels',
    label: 'Channels',
    category: 'Media',
    accessor: (b) => b.channels,
    formatter: formatNumber,
    sortKey: 'channels',
    searchKey: 'channels',
    defaultWidth: 90,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'bit_depth',
    label: 'Bit Depth',
    category: 'Media',
    accessor: (b) => b.bit_depth,
    formatter: (v) => (v != null && typeof v === 'number' ? `${v}-bit` : ''),
    sortKey: 'bit_depth',
    searchKey: 'bit_depth',
    defaultWidth: 90,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'quality',
    label: 'Quality',
    category: 'Media',
    accessor: (b) => b.quality,
    sortKey: 'quality',
    searchKey: 'quality',
    defaultWidth: 140,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'file_path',
    label: 'File Path',
    category: 'Media',
    accessor: (b) => b.file_path,
    sortKey: 'file_path',
    searchKey: 'file_path',
    defaultWidth: 300,
    minWidth: 120,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'original_filename',
    label: 'Original Filename',
    category: 'Media',
    accessor: (b) => b.original_filename,
    sortKey: 'original_filename',
    searchKey: 'original_filename',
    defaultWidth: 220,
    minWidth: 100,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'track_number',
    label: 'Track #',
    category: 'Media',
    accessor: (b) => b.track_number,
    sortKey: 'track_number',
    searchKey: 'track_number',
    defaultWidth: 80,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'total_tracks',
    label: 'Total Tracks',
    category: 'Media',
    accessor: (b) => b.total_tracks,
    formatter: formatNumber,
    sortKey: 'total_tracks',
    searchKey: 'total_tracks',
    defaultWidth: 100,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'disk_number',
    label: 'Disk #',
    category: 'Media',
    accessor: (b) => b.disk_number,
    sortKey: 'disk_number',
    searchKey: 'disk_number',
    defaultWidth: 80,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'total_disks',
    label: 'Total Disks',
    category: 'Media',
    accessor: (b) => b.total_disks,
    formatter: formatNumber,
    sortKey: 'total_disks',
    searchKey: 'total_disks',
    defaultWidth: 100,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },

  // ---- iTunes ----
  {
    id: 'cover_path',
    label: 'Cover Path',
    category: 'iTunes',
    accessor: (b) => b.cover_path,
    sortKey: 'cover_path',
    searchKey: 'cover_path',
    defaultWidth: 200,
    minWidth: 100,
    sortable: false,
    defaultVisible: false,
  },
  {
    id: 'narrators_json',
    label: 'Narrators (JSON)',
    category: 'iTunes',
    accessor: (b) => b.narrators_json,
    sortKey: 'narrators_json',
    searchKey: 'narrators_json',
    defaultWidth: 200,
    minWidth: 100,
    sortable: false,
    defaultVisible: false,
  },

  // ---- Lifecycle ----
  {
    id: 'library_state',
    label: 'Library State',
    category: 'Lifecycle',
    accessor: (b) => b.library_state,
    sortKey: 'library_state',
    searchKey: 'library_state',
    defaultWidth: 120,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'read_status',
    label: 'Read Status',
    category: 'Lifecycle',
    accessor: () => '',
    sortKey: 'read_status',
    searchKey: 'read_status',
    defaultWidth: 120,
    minWidth: 80,
    sortable: false,
    defaultVisible: false,
  },
  {
    id: 'created_at',
    label: 'Created',
    category: 'Lifecycle',
    accessor: (b) => b.created_at,
    formatter: formatDate,
    sortKey: 'created_at',
    searchKey: 'created_at',
    defaultWidth: 120,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'updated_at',
    label: 'Updated',
    category: 'Lifecycle',
    accessor: (b) => b.updated_at,
    formatter: formatDate,
    sortKey: 'updated_at',
    searchKey: 'updated_at',
    defaultWidth: 120,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'metadata_review_status',
    label: 'Review Status',
    category: 'Lifecycle',
    accessor: (b) => b.metadata_review_status,
    sortKey: 'metadata_review_status',
    searchKey: 'review_status',
    defaultWidth: 120,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'marked_for_deletion',
    label: 'Marked for Deletion',
    category: 'Lifecycle',
    accessor: (b) => b.marked_for_deletion,
    formatter: formatBoolean,
    sortKey: 'marked_for_deletion',
    searchKey: 'marked_for_deletion',
    defaultWidth: 140,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'marked_for_deletion_at',
    label: 'Deletion Date',
    category: 'Lifecycle',
    accessor: (b) => b.marked_for_deletion_at,
    formatter: formatDate,
    sortKey: 'marked_for_deletion_at',
    searchKey: 'deletion_date',
    defaultWidth: 120,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'organize_error',
    label: 'Organize Error',
    category: 'Lifecycle',
    accessor: (b) => b.organize_error,
    sortKey: 'organize_error',
    searchKey: 'organize_error',
    defaultWidth: 200,
    minWidth: 100,
    sortable: false,
    defaultVisible: false,
  },
  {
    id: 'quantity',
    label: 'Quantity',
    category: 'Lifecycle',
    accessor: (b) => b.quantity,
    formatter: formatNumber,
    sortKey: 'quantity',
    searchKey: 'quantity',
    defaultWidth: 80,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },

  // ---- Version Management ----
  {
    id: 'is_primary_version',
    label: 'Primary Version',
    category: 'Lifecycle',
    accessor: (b) => b.is_primary_version,
    formatter: formatBoolean,
    sortKey: 'is_primary_version',
    searchKey: 'is_primary',
    defaultWidth: 120,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'version_group_id',
    label: 'Version Group',
    category: 'Lifecycle',
    accessor: (b) => b.version_group_id,
    sortKey: 'version_group_id',
    searchKey: 'version_group',
    defaultWidth: 140,
    minWidth: 80,
    sortable: false,
    defaultVisible: false,
  },
  {
    id: 'version_notes',
    label: 'Version Notes',
    category: 'Lifecycle',
    accessor: (b) => b.version_notes,
    sortKey: 'version_notes',
    searchKey: 'version_notes',
    defaultWidth: 180,
    minWidth: 80,
    sortable: false,
    defaultVisible: false,
  },

  // ---- Fingerprinting ----
  {
    id: 'fingerprint_status',
    label: 'Fingerprint Status',
    category: 'Fingerprinting',
    accessor: (b) => b.fingerprint_status || 'none',
    sortKey: 'fingerprint_status',
    searchKey: 'fingerprint_status',
    defaultWidth: 140,
    minWidth: 100,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'coverage_percent',
    label: 'Coverage',
    category: 'Fingerprinting',
    accessor: (b) => {
      const fp = b.fingerprinted_file_count || 0;
      const total = b.total_file_count || 0;
      const pct = b.coverage_percent || 0;
      return `${fp}/${total} (${pct}%)`;
    },
    sortKey: 'coverage_percent',
    searchKey: 'coverage',
    defaultWidth: 110,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'fingerprinted_files',
    label: 'Fingerprinted',
    category: 'Fingerprinting',
    accessor: (b) => {
      const fp = b.fingerprinted_file_count || 0;
      const total = b.total_file_count || 0;
      return `${fp}/${total}`;
    },
    sortKey: 'fingerprinted_file_count',
    searchKey: 'fingerprinted',
    defaultWidth: 100,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'last_fingerprinted_date',
    label: 'Last Fingerprinted',
    category: 'Fingerprinting',
    accessor: (b) => b.last_fingerprinted_at || '',
    formatter: formatRelativeDate,
    sortKey: 'last_fingerprinted_at',
    searchKey: 'last_fingerprinted',
    defaultWidth: 140,
    minWidth: 100,
    sortable: true,
    defaultVisible: false,
  },

  // ---- IDs ----
  {
    id: 'id',
    label: 'ID',
    category: 'IDs',
    accessor: (b) => b.id,
    sortKey: 'id',
    searchKey: 'id',
    defaultWidth: 100,
    minWidth: 60,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'isbn10',
    label: 'ISBN-10',
    category: 'IDs',
    accessor: (b) => b.isbn10,
    sortKey: 'isbn10',
    searchKey: 'isbn10',
    defaultWidth: 120,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'isbn13',
    label: 'ISBN-13',
    category: 'IDs',
    accessor: (b) => b.isbn13,
    sortKey: 'isbn13',
    searchKey: 'isbn13',
    defaultWidth: 140,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'work_id',
    label: 'Work ID',
    category: 'IDs',
    accessor: (b) => b.work_id,
    sortKey: 'work_id',
    searchKey: 'work_id',
    defaultWidth: 120,
    minWidth: 80,
    sortable: true,
    defaultVisible: false,
  },
  {
    id: 'file_hash',
    label: 'File Hash',
    category: 'IDs',
    accessor: (b) => b.file_hash,
    sortKey: 'file_hash',
    searchKey: 'file_hash',
    defaultWidth: 160,
    minWidth: 80,
    sortable: false,
    defaultVisible: false,
  },
  {
    id: 'original_file_hash',
    label: 'Original Hash',
    category: 'IDs',
    accessor: (b) => b.original_file_hash,
    sortKey: 'original_file_hash',
    searchKey: 'original_hash',
    defaultWidth: 160,
    minWidth: 80,
    sortable: false,
    defaultVisible: false,
  },
  {
    id: 'organized_file_hash',
    label: 'Organized Hash',
    category: 'IDs',
    accessor: (b) => b.organized_file_hash,
    sortKey: 'organized_file_hash',
    searchKey: 'organized_hash',
    defaultWidth: 160,
    minWidth: 80,
    sortable: false,
    defaultVisible: false,
  },
];

// --- Lookup helpers ---

/** Map from column ID to definition for O(1) lookup */
export const COLUMN_MAP: ReadonlyMap<string, ColumnDefinition> = new Map(
  ALL_COLUMNS.map((col) => [col.id, col])
);

/** Get columns that are visible by default */
export function getDefaultVisibleColumns(): ColumnDefinition[] {
  return ALL_COLUMNS.filter((col) => col.defaultVisible);
}

/** Get columns grouped by category */
export function getColumnsByCategory(): Record<string, ColumnDefinition[]> {
  const grouped: Record<string, ColumnDefinition[]> = {};
  for (const col of ALL_COLUMNS) {
    if (!grouped[col.category]) grouped[col.category] = [];
    grouped[col.category].push(col);
  }
  return grouped;
}

/** Get a column definition by ID, or undefined if not found */
export function getColumnById(id: string): ColumnDefinition | undefined {
  return COLUMN_MAP.get(id);
}

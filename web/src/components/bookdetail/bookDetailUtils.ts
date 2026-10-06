// file: web/src/components/bookdetail/bookDetailUtils.ts
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-10-06

export const formatDateTime = (value?: string) => {
  if (!value) return '—';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
};

export const formatDuration = (seconds?: number) => {
  if (!seconds || seconds <= 0) return '—';
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainingSeconds = Math.floor(seconds % 60);
  const parts: string[] = [];
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0 || hours > 0) parts.push(`${minutes}m`);
  if (remainingSeconds > 0 && hours === 0) parts.push(`${remainingSeconds}s`);
  return parts.join(' ');
};

export const formatBytes = (bytes?: number) => {
  if (!bytes || bytes <= 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  const decimals = value >= 10 || unitIndex === 0 ? 0 : 1;
  return `${value.toFixed(decimals)} ${units[unitIndex]}`;
};

export const formatTagValue = (value?: string | number | boolean | null) => {
  if (value == null || value === '') return '—';
  return String(value);
};

/**
 * What the UI knows about one file on disk. `file_exists` is a live stat made
 * for the response: true/false when it answered, null when it could not
 * (permission or I/O error, a timeout, a suspended root, or `disk_check=false`).
 * Null is NOT "present": the stored `missing` flag is then the best evidence,
 * so a stored-missing row still shows as missing and still offers relocate.
 */
export type FileDiskStatus = 'present' | 'missing' | 'unknown-missing' | 'unknown';

export interface FileDiskFields {
  file_exists?: boolean | null;
  missing?: boolean;
  active?: boolean;
}

export function fileDiskStatus(f: FileDiskFields): FileDiskStatus {
  if (f.file_exists === true) return 'present';
  if (f.file_exists === false) return 'missing';
  // Legacy rows may carry only `active` (= !missing) and not `missing`.
  const storedMissing = f.missing ?? (f.active === undefined ? false : !f.active);
  return storedMissing ? 'unknown-missing' : 'unknown';
}

/** Missing for the badge, the red row and the relocate prompt. */
export function isFileMissing(f: FileDiskFields): boolean {
  const s = fileDiskStatus(f);
  return s === 'missing' || s === 'unknown-missing';
}

/** The disk check did not answer for this file. */
export function isFileDiskUnknown(f: FileDiskFields): boolean {
  const s = fileDiskStatus(f);
  return s === 'unknown' || s === 'unknown-missing';
}

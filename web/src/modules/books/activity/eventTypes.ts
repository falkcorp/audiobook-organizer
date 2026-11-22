// file: web/src/modules/books/activity/eventTypes.ts
// version: 0.1.0
// guid: 7d2a9e5c-6b18-4f43-8a01-c9e4b3d7f265
// last-edited: 2026-11-22

/**
 * The event types of the audiobook activity feed: what the Activity page's Type
 * filter offers and what `?type=` accepts. They are the audiobook module's, so they
 * live here and the frame takes them as a binding (it used to hold this list in
 * pages/activityUrl.ts, which now re-exports it from here for its importers).
 */
export const ACTIVITY_EVENT_TYPES: readonly string[] = [
  'book_added',
  'book_updated',
  'book_deleted',
  'book_restored',
  'tag_written',
  'metadata_applied',
  'scan_started',
  'scan_completed',
  'organize_started',
  'organize_completed',
  'import_started',
  'import_completed',
  'maintenance_run',
  'config_changed',
  'user_action',
];

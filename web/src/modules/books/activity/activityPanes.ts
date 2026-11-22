// file: web/src/modules/books/activity/activityPanes.ts
// version: 0.1.0
// guid: 91c4e7a3-0d6b-4f28-85e2-b7a1d93c5f40
// last-edited: 2026-11-22

import type { ActivityPane } from '../../../components/activity/activityFrameTypes';
import { PendingFileOpsPane } from './PendingFileOpsPane';

/**
 * The books panes of the Activity page, in the order they render. Each takes no
 * props and has an id and a label, so each becomes one `activityPanes` entry
 * (`{ id, label, lazy: () => import('./PendingFileOpsPane') }`) once the
 * contribution type resolves in `web`.
 */
export const BOOKS_ACTIVITY_PANES: readonly ActivityPane[] = [
  { id: 'pending-file-ops', label: 'Pending file operations', Component: PendingFileOpsPane },
];

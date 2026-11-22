// file: web/src/pages/ActivityLog.tsx
// version: 2.42.1
// guid:b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-11-07

import { ActivityLogFrame } from '../components/activity/ActivityLogFrame';
import { booksActivityBindings } from '../modules/books/activity/activityBindings';

/**
 * The Activity page as ABO has always served it: the shell's frame
 * (components/activity/ActivityLogFrame.tsx) bound to the legacy operations store,
 * the legacy services and the books pieces (modules/books/activity/). F9-03 mounts
 * the frame as the shell's Activity page with the books `activityPanes` and
 * `taskKinds` contributions and deletes this file.
 */
export default function ActivityLog() {
  return <ActivityLogFrame bindings={booksActivityBindings} />;
}

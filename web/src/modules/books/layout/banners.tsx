// file: web/src/modules/books/layout/banners.tsx
// version: 0.1.0
// guid: 3a9d6e15-8c42-4b07-a1f8-d2e5b7c0f946
// last-edited: 2026-11-21

import { ReviewBanner } from '../../../components/ReviewBanner';
import { usePendingFileOps } from '../../../hooks/usePendingFileOps';

/**
 * Global poller: fires the "All tag writes complete" toast on every page even
 * if the user is not on Activity Log. Renders nothing; it is a banner-slot
 * entry because the slot is where a module mounts layout-wide behaviour (a
 * banner that is idle renders null, which is this one's only state).
 * MainLayout called this hook directly before the layout became a frame.
 */
function PendingFileOpsWatcher() {
  usePendingFileOps({ fireToasts: true });
  return null;
}

/** What the books module mounts above every page: the review banner and the file-ops watcher. */
export function BooksBanners() {
  return (
    <>
      <PendingFileOpsWatcher />
      <ReviewBanner />
    </>
  );
}

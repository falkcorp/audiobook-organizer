// file: web/src/modules/books/layout/useReviewCount.ts
// version: 0.1.0
// guid: 6d1e8a24-9b53-4c70-b3f2-0a7c5e9d4b18
// last-edited: 2026-11-21

import { useReviewStore } from '../../../stores/useReviewStore';

/**
 * The pending-review count behind the Review entry's badge. A hook (FE-22): the
 * layout calls it inside a component of its own, so the count is read in
 * render and follows the store live.
 */
export const useReviewCount = () => useReviewStore((s) => s.count);

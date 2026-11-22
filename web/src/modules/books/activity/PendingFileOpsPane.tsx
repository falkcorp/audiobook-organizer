// file: web/src/modules/books/activity/PendingFileOpsPane.tsx
// version: 0.1.0
// guid: 5b1d8f37-c4a2-4e69-b0d6-93e7a2c8140f
// last-edited: 2026-11-22

import { PendingFileOpsBanner } from '../../../components/PendingFileOpsBanner';
import { usePendingFileOps } from '../../../hooks/usePendingFileOps';

/** The background file operations still in flight (cover embed, tag write, rename). Takes no props. */
export function PendingFileOpsPane() {
  const { operations } = usePendingFileOps();
  return <PendingFileOpsBanner operations={operations} />;
}

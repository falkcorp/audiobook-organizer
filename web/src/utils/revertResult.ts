// file: web/src/utils/revertResult.ts
// version: 1.0.0
// guid: 05f380f7-79a1-4953-8339-85979484c6a2
// last-edited: 2026-09-12

import type { RevertOperationResult } from '../services/api';

const capitalize = (s: string): string => s.charAt(0).toUpperCase() + s.slice(1);

/**
 * The line to show the user after an operation revert returns 200. A partial
 * result (some rows failed or cannot be undone automatically) must never read
 * as a success, so the server's own summary is shown whenever it is present.
 */
export function describeRevertResult(result: Partial<RevertOperationResult> | null | undefined): string {
  if (result?.partial) {
    if (result.message) return capitalize(result.message);
    return `Operation only partially reverted: ${result.restored ?? 0} of ${result.total ?? 0} changes restored`;
  }
  if (result?.message) return capitalize(result.message);
  return 'Operation reverted';
}

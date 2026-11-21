// file: web/src/shellAppId.ts
// version: 0.1.0
// guid: 7c2d9e41-5a38-4b06-9f1e-d3a8b0c6e752
// last-edited: 2026-11-21

import { setShellAppId } from '@mediacore/shell';

// ABO's app ID: books.AppID in Go and the `appId` F9-03 gives mediacore() and
// createShell (07 §7.10). Shell-stored keys are `${id}.${scope}.${name}`, so
// this must not change at the F9-03 flip.
export const SHELL_APP_ID = 'abo';

/** Sets the shell's app ID; main.tsx calls it before the first render. */
export function initShellAppId(): void {
  setShellAppId(SHELL_APP_ID);
}

// file: core/web/packages/ui-kit/src/shortcuts/useKeyboardShortcuts.ts
// version: 0.1.0
// guid: 6a4d8c31-7f2e-4b59-9e16-d0b3a5f7c842
// last-edited: 2026-11-20

import { useEffect, useRef } from 'react';

/** One "g <key>" chord: the key and what it does. */
export interface GoShortcut {
  key: string; // one lower-case letter
  run: () => void;
}

/** How long after "g" the second key still completes the chord. */
export const CHORD_TIMEOUT_MS = 1500;

/** True when the event comes from somewhere a key press is text, not a command. */
function typing(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  const tag = target.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target.isContentEditable;
}

/**
 * Listens for "g <key>" chords and "?" on the document. The table comes from
 * the caller: this hook names no route (the shell builds the table from its
 * own routes plus each nav entry's goKey, 07 §7.4.2, FE8-5). Keys typed into
 * an input, a textarea, a select or an editable element are ignored, as are
 * chords with a modifier.
 */
export function useKeyboardShortcuts(table: readonly GoShortcut[], onHelp?: () => void): void {
  const tableRef = useRef(table);
  const helpRef = useRef(onHelp);
  useEffect(() => {
    tableRef.current = table;
    helpRef.current = onHelp;
  });
  useEffect(() => {
    let pendingSince = 0;
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || typing(e.target)) return;
      const now = Date.now();
      if (pendingSince && now - pendingSince <= CHORD_TIMEOUT_MS) {
        pendingSince = 0;
        const hit = tableRef.current.find((s) => s.key === e.key.toLowerCase());
        if (hit) {
          e.preventDefault();
          hit.run();
        }
        return;
      }
      pendingSince = 0;
      if (e.key === 'g') {
        pendingSince = now;
      } else if (e.key === '?' && helpRef.current) {
        e.preventDefault();
        helpRef.current();
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, []);
}

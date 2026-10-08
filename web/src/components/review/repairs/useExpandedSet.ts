// file: web/src/components/review/repairs/useExpandedSet.ts
// version: 1.0.0
// guid: b2f39555-7ee9-48a7-870f-f419e8d82113
// last-edited: 2026-10-08

import { useCallback, useState } from 'react';

/** Which items are open, by id (any number at once). Local view state only. */
export function useExpandedSet() {
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  const toggle = useCallback((id: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);
  return { expanded, toggle };
}

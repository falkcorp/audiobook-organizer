// file: web/src/hooks/useAdvancedSettings.ts
// version: 1.0.0
// guid: e7f8a9b0-c1d2-3456-efab-456789012345
// last-edited: 2026-06-15

import { useState, useCallback } from 'react';

const STORAGE_KEY = 'settings.showAdvanced';

export function useAdvancedSettings() {
  const [showAdvanced, setShowAdvanced] = useState<boolean>(() => {
    return localStorage.getItem(STORAGE_KEY) === 'true';
  });

  const toggleAdvanced = useCallback(() => {
    setShowAdvanced(prev => {
      const next = !prev;
      localStorage.setItem(STORAGE_KEY, String(next));
      return next;
    });
  }, []);

  return { showAdvanced, toggleAdvanced };
}

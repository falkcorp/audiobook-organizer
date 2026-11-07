// file: web/src/hooks/useKeyboardShortcuts.ts
// version: 1.0.3
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-11-07

import { useEffect, useCallback, useRef } from 'react';
import { useNavigate } from 'react-router';

export interface ShortcutDefinition {
  keys: string;
  description: string;
  category: string;
}

export const SHORTCUTS: ShortcutDefinition[] = [
  { keys: '/ or Ctrl+K', description: 'Focus search input', category: 'Navigation' },
  { keys: 'g l', description: 'Go to Library', category: 'Navigation' },
  { keys: 'g s', description: 'Go to Settings', category: 'Navigation' },
  { keys: '?', description: 'Show keyboard shortcuts', category: 'General' },
];

function isInputFocused(): boolean {
  const el = document.activeElement;
  if (!el) return false;
  const tag = el.tagName.toLowerCase();
  if (tag === 'input' || tag === 'textarea' || tag === 'select') return true;
  if ((el as HTMLElement).isContentEditable) return true;
  return false;
}

interface UseKeyboardShortcutsOptions {
  onShowHelp: () => void;
}

export function useKeyboardShortcuts({ onShowHelp }: UseKeyboardShortcutsOptions) {
  const navigate = useNavigate();
  const pendingKey = useRef<string | null>(null);
  const pendingTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);

  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
    };
  }, []);

  const clearPending = useCallback(() => {
    pendingKey.current = null;
    if (pendingTimer.current) {
      clearTimeout(pendingTimer.current);
      pendingTimer.current = null;
    }
  }, []);

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      // Allow Ctrl+K even in inputs (standard search shortcut)
      if (e.key === 'k' && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        const searchInput = document.querySelector<HTMLInputElement>(
          'input[placeholder*="earch"], input[aria-label*="earch"]'
        );
        searchInput?.focus();
        searchInput?.select();
        clearPending();
        return;
      }

      if (isInputFocused()) return;

      // Handle second key of a chord
      if (pendingKey.current === 'g') {
        clearPending();
        if (e.key === 'l') {
          e.preventDefault();
          navigate('/library');
          return;
        }
        if (e.key === 's') {
          e.preventDefault();
          navigate('/settings');
          return;
        }
        return;
      }

      // Start chord
      if (e.key === 'g') {
        e.preventDefault();
        pendingKey.current = 'g';
        if (!isUnmountedRef.current) {
          pendingTimer.current = setTimeout(clearPending, 1000);
        }
        return;
      }

      if (e.key === '/') {
        e.preventDefault();
        const searchInput = document.querySelector<HTMLInputElement>(
          'input[placeholder*="earch"], input[aria-label*="earch"]'
        );
        searchInput?.focus();
        searchInput?.select();
        return;
      }

      if (e.key === '?') {
        e.preventDefault();
        onShowHelp();
        return;
      }
    };

    window.addEventListener('keydown', handler);
    return () => {
      window.removeEventListener('keydown', handler);
      clearPending();
    };
  }, [navigate, onShowHelp, clearPending]);
}

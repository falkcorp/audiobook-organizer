// file: core/web/packages/shell/test/shortcuts.test.tsx
// version: 0.1.0
// guid: 9f2b6d03-4c81-4e57-a0d9-6b3e8f1c7a42
// last-edited: 2026-11-20

// 07 §7.18 (FE8-5): "g s" goes to /settings, a module nav entry's goKey goes
// to its page, a second entry with a used key is dropped with a warning, and
// ui-kit's useKeyboardShortcuts names no route.

import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { act, fireEvent } from '@testing-library/react';
import { useMemo } from 'react';
import { useNavigate } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import { useKeyboardShortcuts } from '@mediacore/ui-kit';
import { grantsFor } from '../src/auth/hooks';
import { buildGoTable, buildNav } from '../src/nav/navModel';
import { renderInShell } from '../src/testing';
import type { ModuleUI } from '../src/types';
import { arkham, batcave } from './fixtures';

function Shortcuts({ modules }: { modules: readonly ModuleUI[] }) {
  const navigate = useNavigate();
  const table = useMemo(
    () => buildGoTable(buildNav(modules, grantsFor(['batcave:read', 'arkham:read']))).map((e) => ({ key: e.key, run: () => navigate(e.href) })),
    [modules, navigate],
  );
  useKeyboardShortcuts(table);
  return null;
}

function chord(second: string) {
  act(() => {
    fireEvent.keyDown(document, { key: 'g' });
    fireEvent.keyDown(document, { key: second });
  });
}

describe('shortcuts', () => {
  it('"g s" goes to /settings: the shell key wins over a module that claims s', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const modules = [batcave()];
    const { router } = renderInShell(<Shortcuts modules={modules} />, { modules, permissions: ['batcave:read'] });
    chord('s');
    expect(router.state.location.pathname).toBe('/settings');
    expect(warn).toHaveBeenCalledWith(expect.stringMatching(/batcave\.vehicles: goKey "s" is already used by the shell/));
    warn.mockRestore();
  });

  it("a nav entry's goKey goes to its page; a later module's duplicate is dropped with a warning", () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const modules = [arkham(), batcave()];
    const { router } = renderInShell(<Shortcuts modules={modules} />, { modules, permissions: ['batcave:read', 'arkham:read'] });
    // Module ID order: arkham comes first and claims 'l'.
    chord('l');
    expect(router.state.location.pathname).toBe('/arkham/inmates');
    expect(warn).toHaveBeenCalledWith(expect.stringMatching(/batcave\.gadgets: goKey "l" is already used by arkham\.inmates/));
    warn.mockRestore();
  });

  it('ignores chords typed into an input', () => {
    const modules = [batcave()];
    const { router } = renderInShell(
      <>
        <Shortcuts modules={modules} />
        <input aria-label="search" />
      </>,
      { modules, permissions: ['batcave:read'] },
    );
    const input = document.querySelector('input')!;
    act(() => {
      fireEvent.keyDown(input, { key: 'g' });
      fireEvent.keyDown(input, { key: 'l' });
    });
    expect(router.state.location.pathname).toBe('/');
  });

  it("ui-kit's hook names no route", () => {
    const src = readFileSync(join(import.meta.dirname, '../../ui-kit/src/shortcuts/useKeyboardShortcuts.ts'), 'utf8');
    expect(src).not.toMatch(/['"`]\/[a-z]/);
  });
});

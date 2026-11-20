// file: core/web/packages/shell/test/nav.permissions.test.tsx
// version: 0.1.0
// guid: 0c5d8e31-7a64-4b29-9f80-d3e6b1a7c254
// last-edited: 2026-11-20

// 07 §7.18: a nav entry is hidden without its permission (and a module
// without its permissionsHints.view), and useBadge runs inside <NavBadge>.

import { screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { buildNav } from '../src/nav/navModel';
import { ShellNav } from '../src/nav/ShellNav';
import { renderInShell } from '../src/testing';
import { batcave } from './fixtures';

describe('nav permissions', () => {
  it('hides an entry whose permission the user lacks, and shows it to a holder', () => {
    const reader = buildNav([batcave()], ['batcave:read']);
    expect(reader.map((i) => i.label)).toEqual(['Gadgets', 'Vehicles']);
    const admin = buildNav([batcave()], ['batcave:read', 'batcave:admin']);
    expect(admin.map((i) => i.label)).toContain('Secret lab');
    // A namespace pattern covers it, as rbac.Covers does.
    expect(buildNav([batcave()], ['batcave:*']).map((i) => i.label)).toContain('Secret lab');
  });

  it('hides the whole module without any permissionsHints.view permission', () => {
    expect(buildNav([batcave()], [])).toEqual([]);
  });

  it('resolves page links under /<module id>/ with the search', () => {
    const items = buildNav([batcave()], ['batcave:read']);
    expect(items.find((i) => i.label === 'Vehicles')?.href).toBe('/batcave/vehicles/:id?sort=speed');
    expect(items.find((i) => i.label === 'Gadgets')?.href).toBe('/batcave/gadgets');
  });

  it('calls useBadge inside a NavBadge and shows its count', async () => {
    const useBadge = vi.fn(() => 7);
    const mod = batcave(useBadge);
    renderInShell(<ShellNav items={buildNav([mod], ['batcave:read'])} />, { modules: [mod], permissions: ['batcave:read'] });
    expect(await screen.findByText('7')).toBeInTheDocument();
    expect(screen.getAllByTestId('nav-badge')).toHaveLength(1);
    expect(useBadge).toHaveBeenCalled();
    expect(screen.queryByText('Secret lab')).not.toBeInTheDocument();
  });
});

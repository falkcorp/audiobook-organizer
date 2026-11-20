// file: core/web/packages/shell/test/nav.permissions.test.tsx
// version: 0.1.0
// guid: 0c5d8e31-7a64-4b29-9f80-d3e6b1a7c254
// last-edited: 2026-11-20

// 07 §7.18: a nav entry is hidden without its permission (and a module
// without its permissionsHints.view), and useBadge runs inside <NavBadge>.

import { screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { MainLayout } from '../src/app/MainLayout';
import { grantsFor, grantsOf } from '../src/auth/hooks';
import { buildNav } from '../src/nav/navModel';
import { ShellNav } from '../src/nav/ShellNav';
import { renderInShell } from '../src/testing';
import { batcave } from './fixtures';

describe('nav permissions', () => {
  it('hides an entry whose permission the user lacks, and shows it to a holder', () => {
    const reader = buildNav([batcave()], grantsFor(['batcave:read']));
    expect(reader.map((i) => i.label)).toEqual(['Gadgets', 'Vehicles']);
    const admin = buildNav([batcave()], grantsFor(['batcave:read', 'batcave:admin']));
    expect(admin.map((i) => i.label)).toContain('Secret lab');
    // A namespace pattern covers it, as rbac.Covers does.
    expect(buildNav([batcave()], grantsFor(['batcave:*'])).map((i) => i.label)).toContain('Secret lab');
  });

  it('hides the whole module without any permissionsHints.view permission', () => {
    expect(buildNav([batcave()], grantsFor([]))).toEqual([]);
  });

  it('resolves page links under /<module id>/ with the search', () => {
    const items = buildNav([batcave()], grantsFor(['batcave:read']));
    expect(items.find((i) => i.label === 'Vehicles')?.href).toBe('/batcave/vehicles/:id?sort=speed');
    expect(items.find((i) => i.label === 'Gadgets')?.href).toBe('/batcave/gadgets');
  });

  it('calls useBadge inside a NavBadge and shows its count', async () => {
    const useBadge = vi.fn(() => 7);
    const mod = batcave(useBadge);
    renderInShell(<ShellNav items={buildNav([mod], grantsFor(['batcave:read']))} />, { modules: [mod], permissions: ['batcave:read'] });
    expect(await screen.findByText('7')).toBeInTheDocument();
    expect(screen.getAllByTestId('nav-badge')).toHaveLength(1);
    expect(useBadge).toHaveBeenCalled();
    expect(screen.queryByText('Secret lab')).not.toBeInTheDocument();
  });
});

describe('auth-disabled mode (07 §7.9.1)', () => {
  it('grants everything to the principal when the server enforces nothing, and nothing when signed out', () => {
    const base = { status: 'authenticated', user: null, permissions: [], method: null, ownerVerified: false, setupRequired: false, error: null, refresh: async () => {}, logout: async () => {} } as const;
    expect(grantsOf({ ...base, authDisabled: true })('batcave:admin')).toBe(true);
    expect(grantsOf({ ...base, authDisabled: true })('core:admin')).toBe(true);
    expect(grantsOf({ ...base, authDisabled: false })('batcave:admin')).toBe(false);
    expect(grantsOf({ ...base, status: 'anonymous', authDisabled: true })('batcave:read')).toBe(false);
  });

  it('shows permission-gated module nav and shell entries with an empty permission list', async () => {
    const mod = batcave();
    renderInShell(<MainLayout />, { modules: [mod], auth: { authDisabled: true, user: null, permissions: [] } });
    expect(await screen.findByText('Secret lab')).toBeInTheDocument();
    expect(screen.getByText('Gadgets')).toBeInTheDocument();
    expect(screen.getByText('Users')).toBeInTheDocument();
  });

  it('hides them for a signed-in user without the permissions (auth enabled)', async () => {
    const mod = batcave();
    renderInShell(<MainLayout />, { modules: [mod], permissions: [] });
    expect(await screen.findByText('Activity')).toBeInTheDocument();
    expect(screen.queryByText('Secret lab')).not.toBeInTheDocument();
    expect(screen.queryByText('Gadgets')).not.toBeInTheDocument();
    expect(screen.queryByText('Users')).not.toBeInTheDocument();
  });
});

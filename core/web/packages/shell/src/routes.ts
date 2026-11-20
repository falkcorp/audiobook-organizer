// file: core/web/packages/shell/src/routes.ts
// version: 0.1.0
// guid: 0c4f8b27-1e93-4a65-8d70-5b2e9c6a1f38
// last-edited: 2026-11-20

// The shell's own routes (07 §7.4.3). Their first segments are exactly Go's
// ui.ReservedSegments() (reservedSegments.test.ts reads core/ui/leaf.go), so
// a module ID can never shadow a shell page. There is no /logout page: signing
// out is a user-menu action (FE2-7).

export type ShellRouteId =
  | 'home'
  | 'login'
  | 'loginTemp'
  | 'setup'
  | 'invite'
  | 'activity'
  | 'settings'
  | 'system'
  | 'users'
  | 'notFound';

export interface ShellRoute {
  id: ShellRouteId;
  path: string;
  /** False for the pages an anonymous visitor reaches. */
  auth: boolean;
}

export const SHELL_ROUTES: readonly ShellRoute[] = [
  { id: 'home', path: '/', auth: true },
  { id: 'login', path: '/login', auth: false },
  { id: 'loginTemp', path: '/login/temp', auth: false },
  { id: 'setup', path: '/setup', auth: false },
  { id: 'invite', path: '/invite', auth: false },
  { id: 'activity', path: '/activity', auth: true },
  { id: 'settings', path: '/settings/*', auth: true },
  { id: 'system', path: '/system/*', auth: true },
  { id: 'users', path: '/users', auth: true },
  { id: 'notFound', path: '*', auth: false },
];

/** The first path segment of every shell route, sorted: Go's ui.ReservedSegments(). */
export function shellReservedSegments(): string[] {
  const segs = new Set<string>();
  for (const r of SHELL_ROUTES) {
    const first = r.path.split('/')[1];
    if (first && first !== '*') segs.add(first);
  }
  return [...segs].sort();
}

/** The "g <key>" chords of the shell's own pages (FE8-5). */
export const SHELL_GO_KEYS: Readonly<Record<string, string>> = {
  h: '/',
  a: '/activity',
  s: '/settings',
  y: '/system',
  u: '/users',
};

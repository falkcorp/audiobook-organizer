// file: core/web/packages/shell/src/auth/hooks.ts
// version: 0.1.0
// guid: 87721002-3daa-48ae-96b9-7a70e61ebdf7
// last-edited: 2026-11-20

import { useContext } from 'react';
import type { AuthState } from '../contracts';
import type { Permission } from '../types';
import type { ShellClient } from '../api/interim';
import { AuthClientContext, AuthContext, ModulesHintsContext } from './context';

export function useAuth(): AuthState {
  const value = useContext(AuthContext);
  if (!value) throw new Error('useAuth must be used inside <AuthProvider>');
  return value;
}

/**
 * Whether `held` grants `wanted`, by rbac.Covers (core/rbac/store.go): a concrete
 * permission covers itself, "<ns>:*" every scope of ns, "<ns>:<action>.*" the bare
 * action and every sub-action of it. GET /me lists concrete scopes, so a pattern
 * is not expected there; honouring one keeps the UI from disagreeing with the
 * server about what a pattern means. Anything malformed grants nothing.
 */
export function covers(held: string, wanted: string): boolean {
  const w = split(wanted);
  const h = split(held);
  if (!w || !h) return false;
  if (h.rest === '*') return h.ns === w.ns;
  if (h.rest.endsWith('.*')) return h.ns === w.ns && h.rest.slice(0, -2) === w.rest.split('.')[0];
  return held === wanted;
}

function split(p: string): { ns: string; rest: string } | null {
  const i = p.indexOf(':');
  if (i <= 0 || i === p.length - 1) return null;
  return { ns: p.slice(0, i), rest: p.slice(i + 1) };
}

/** Pure form of usePermission: false unless signed in and some held permission covers `p`. */
export function hasPermission(auth: AuthState | null, p: Permission): boolean {
  if (!auth || auth.status !== 'authenticated') return false;
  return auth.permissions.some((held) => covers(held, p));
}

/** The client of the enclosing AuthProvider. */
export function useAuthClient(): ShellClient {
  const client = useContext(AuthClientContext);
  if (!client) throw new Error('this page must be rendered inside <AuthProvider>');
  return client;
}

/** Advisory (the server decides): false when unknown, signed out or outside a provider (07 §7.9.1). */
export function usePermission(p: Permission): boolean {
  return hasPermission(useContext(AuthContext), p);
}

/**
 * Whether the signed-in user may do the module's named UI action: the permission
 * `permissionsHints.actions[action]` of module `moduleId`. An unknown module or
 * action is false.
 */
export function useAction(moduleId: string, action: string): boolean {
  const auth = useContext(AuthContext);
  const modules = useContext(ModulesHintsContext);
  const perm = modules.find((m) => m.id === moduleId)?.permissionsHints.actions?.[action];
  return perm !== undefined && hasPermission(auth, perm as Permission);
}

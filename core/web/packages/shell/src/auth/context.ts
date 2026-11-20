// file: core/web/packages/shell/src/auth/context.ts
// version: 0.1.0
// guid: 24bb9ccc-a69a-4182-af37-4547b7ab1196
// last-edited: 2026-11-20

import { createContext } from 'react';
import type { ShellClient } from '../api/interim';
import type { AuthState } from '../contracts';
import type { ModuleUI } from '../types';

/** AuthProvider's value; null outside a provider (useAuth throws, usePermission fails closed). */
export const AuthContext = createContext<AuthState | null>(null);

/**
 * The modules' UI descriptors, whose `permissionsHints.actions` useAction reads.
 * The shell fills it with the mounted `ModuleUI[]`; empty by default, so
 * useAction fails closed without one.
 */
export const ModulesHintsContext = createContext<readonly ModuleUI[]>([]);

/**
 * The client AuthProvider was given, for the pages (login, setup, invite, temp
 * login). They are zero-prop route components loaded with react-router's `lazy`,
 * so the client reaches them through context, not props.
 */
export const AuthClientContext = createContext<ShellClient | null>(null);

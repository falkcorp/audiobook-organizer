// file: core/web/packages/shell/src/app/context.ts
// version: 0.1.0
// guid: 6c1e8a47-3b92-4f05-a7d6-0e4b9c2f5a13
// last-edited: 2026-11-20

import { createContext, useContext } from 'react';
import type { ShellClient } from '../api/client';
import type { Diagnostic } from '../version';
import type { ModuleUI } from '../types';

/** What the shell knows about itself, for its own pages and for module code. */
export interface ShellContextValue {
  appId: string;
  displayName: string;
  client: ShellClient;
  /** Every module compiled into the bundle (ShellConfig.modules). */
  registry: readonly ModuleUI[];
}

export const ShellContext = createContext<ShellContextValue | null>(null);

export function useShellContext(): ShellContextValue {
  const v = useContext(ShellContext);
  if (!v) throw new Error('@mediacore/shell: this hook needs createShell or renderInShell above it');
  return v;
}

/** useApi() of 07 §7.4.2: the shell's API client. */
export function useApi(): ShellClient {
  return useShellContext().client;
}

/** The modules mounted after the intersection (07 §7.5.2) and its diagnostics. */
export interface MountedModulesValue {
  /** idle: nobody is signed in; loading and error: module pages wait or explain instead of 404ing. */
  status: 'idle' | 'loading' | 'error' | 'ready';
  modules: readonly ModuleUI[];
  diagnostics: readonly Diagnostic[];
}

export const MountedModulesContext = createContext<MountedModulesValue>({ status: 'idle', modules: [], diagnostics: [] });

export function useMountedModules(): MountedModulesValue {
  return useContext(MountedModulesContext);
}

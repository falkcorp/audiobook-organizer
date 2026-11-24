// file: core/web/packages/shell/src/api/client.ts
// version: 0.1.0
// guid: e3a94c17-5f80-4d62-b91a-0c7d2e8f4a53
// last-edited: 2026-11-22

// The shell's client: core's generated client, so the auth and profile routes
// are typed by core.json (api-client's `paths` and `coreOperations`) and `public`
// is read from the generated operation table. The two settings routes keep
// their hand-typed shapes (api/settingsInterim.ts) until core.json types them.

import {
  coreOperations,
  createCoreClient,
  createModuleClient,
  type CoreClientOptions,
  type ModuleClient,
  type Problem,
  type paths,
} from '@mediacore/api-client';
import type { SettingsPaths } from './settingsInterim';

/** core's generated paths, with the two settings routes read through their refined shapes. */
export type ShellPaths = Omit<paths, keyof SettingsPaths> & SettingsPaths;

/** An openapi-fetch client over ShellPaths, with the runtime of api-client. */
export type ShellClient = ModuleClient<ShellPaths>;

type UnauthorizedListener = (problem: Problem) => void;

const unauthorizedListeners = new WeakMap<object, Set<UnauthorizedListener>>();

/**
 * Subscribes to the session-ending 401s of `client` (07 §7.9.1). The runtime
 * takes one `onUnauthorized` at creation, before any provider exists, so
 * createShellClient fans it out to the listeners registered here. A client not
 * made by createShellClient has none: the subscription is then a no-op.
 */
export function onShellUnauthorized(client: ShellClient, listener: UnauthorizedListener): () => void {
  const set = unauthorizedListeners.get(client);
  if (!set) return () => {};
  set.add(listener);
  return () => {
    set.delete(listener);
  };
}

/** createCoreClient with the session-ending 401 fan-out, viewed over ShellPaths. */
export function createShellClient(opts: CoreClientOptions): ShellClient {
  const listeners = new Set<UnauthorizedListener>();
  const core = createCoreClient({
    ...opts,
    onUnauthorized: (problem) => {
      opts.onUnauthorized?.(problem);
      for (const l of [...listeners]) l(problem);
    },
  });
  const client = createModuleClient<ShellPaths>(core, { operations: coreOperations });
  unauthorizedListeners.set(client, listeners);
  return client;
}

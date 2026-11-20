// file: core/web/packages/shell/src/app/ModulesGate.tsx
// version: 0.1.0
// guid: 8b3f6e21-5d97-4c40-a1e8-2c7d0f9b4a56
// last-edited: 2026-11-20

import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Alert, Button, Typography } from '@mui/material';
import { BlockingPage } from '@mediacore/ui-kit';
import { grantsOf, useAuth } from '../auth';
import { useResync } from '../realtime';
import { intersect, loadServerModules, reportDiagnostics, type Diagnostic, type IntersectResult } from '../version';
import { MountedModulesContext, useShellContext } from './context';
import { CORE_VERSION } from '../version/coreVersion';

type State =
  | { phase: 'idle' }
  | { phase: 'loading' }
  | { phase: 'error'; message: string }
  | { phase: 'ready'; result: IntersectResult };

/**
 * Runs the intersection of 07 §7.5.2 once after login, again on every
 * stream reset and on a retry: loads /api/core/v1/modules, decides what
 * mounts, reports diagnostics once per kind (System → UI diagnostics shows
 * them), and blocks the app on a core version mismatch. Until a signed-in
 * user exists no module is mounted; the shell's own pages still render.
 */
export function ModulesGate({ children }: { children: ReactNode }) {
  const { appId, client, registry } = useShellContext();
  const auth = useAuth();
  const [state, setState] = useState<State>({ phase: 'idle' });
  const [generation, setGeneration] = useState(0);
  const resync = useCallback(() => setGeneration((g) => g + 1), []);
  useResync(resync);

  const signedIn = auth.status === 'authenticated';
  const isAdmin = grantsOf(auth)('core:admin');
  useEffect(() => {
    if (!signedIn) {
      setState({ phase: 'idle' });
      return;
    }
    const ctl = new AbortController();
    setState((s) => (s.phase === 'ready' ? s : { phase: 'loading' }));
    loadServerModules(client, ctl.signal).then(
      (server) => {
        const result = intersect(CORE_VERSION, server, registry, { isAdmin });
        const diags: Diagnostic[] = [...result.diagnostics];
        if (server.app.id !== appId) {
          diags.push({ kind: 'app-id-mismatch', detail: `the page's app ID is ${appId}, the server's is ${server.app.id}` });
        }
        for (const d of diags) {
          if (d.level !== 'debug') console.warn(`@mediacore/shell: ${d.kind}${d.module ? ` (${d.module})` : ''}: ${d.detail}`);
        }
        const rt = client.runtime;
        void reportDiagnostics((input, init) => rt.fetch(`${rt.baseUrl}${String(input)}`, { ...init, credentials: 'same-origin' }), diags);
        setState({ phase: 'ready', result: { ...result, diagnostics: diags } });
      },
      (err: unknown) => {
        if (ctl.signal.aborted) return;
        setState({ phase: 'error', message: err instanceof Error ? err.message : String(err) });
      },
    );
    return () => ctl.abort();
  }, [signedIn, isAdmin, generation, client, registry, appId]);

  if (state.phase === 'ready' && state.result.blocked) {
    const b = state.result.blocked;
    return (
      <BlockingPage title="UI and server versions do not match">
        <Typography>
          This page was built for core {b.client}; the server runs core {b.server}. Reload once the server and the UI come from the same
          release.
        </Typography>
      </BlockingPage>
    );
  }
  const value =
    state.phase === 'ready'
      ? { status: 'ready' as const, modules: state.result.modules, diagnostics: state.result.diagnostics }
      : { status: state.phase, modules: [], diagnostics: [] };
  return (
    <MountedModulesContext.Provider value={value}>
      {state.phase === 'error' && (
        <Alert
          severity="error"
          data-testid="modules-error"
          action={<Button color="inherit" size="small" onClick={resync}>Retry</Button>}
          sx={{ borderRadius: 0 }}
        >
          Could not load the server's module list: {state.message}. Module pages are unavailable until it loads.
        </Alert>
      )}
      {children}
    </MountedModulesContext.Provider>
  );
}

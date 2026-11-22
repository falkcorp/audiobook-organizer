// file: core/web/packages/shell/src/app/settingsHost.tsx
// version: 0.1.0
// guid: 05d3e022-7f6b-4110-a7d4-2cf1f479cf13
// last-edited: 2026-11-22

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Alert, Box, Button, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, Paper, Stack } from '@mui/material';
import { useBlocker } from 'react-router';
import type { ModuleUI, SettingsSession } from '../types';

interface SessionsValue {
  dirty: boolean;
  saving: boolean;
  /** The reason the last save failed; cleared by the next save or discard. */
  error: string | null;
  /** One save per module with a dirty form, in module ID order; stops at the first failure. */
  saveAll: () => Promise<void>;
  discardAll: () => void;
}

const SessionsContext = createContext<SessionsValue | null>(null);

function useSessions(): SessionsValue {
  const v = useContext(SessionsContext);
  if (!v) throw new Error('@mediacore/shell: settings controls need SettingsSessions above them');
  return v;
}

type Mod = ModuleUI & { settingsSession: NonNullable<ModuleUI['settingsSession']> };

/** The modules that contribute a settings session, in module ID order. */
export function sessionModules(modules: readonly ModuleUI[]): Mod[] {
  return [...modules].filter((m): m is Mod => m.settingsSession !== undefined).sort((a, b) => a.id.localeCompare(b.id));
}

/** Reads one module's session from inside its own Provider and reports it to the page. */
function Reporter({ id, useSession, report }: { id: string; useSession: () => SettingsSession; report: (id: string, s: SettingsSession) => void }) {
  const session = useSession();
  // Every render: the page keeps the latest save and discard, and re-renders only when dirty or saving changes.
  useEffect(() => {
    report(id, session);
  });
  return null;
}

const message = (e: unknown) => (e instanceof Error && e.message ? e.message : String(e));

/**
 * The Settings page's shared sessions (F9-03 prep): every module's Provider
 * wraps the page so its panels, banners, footer and dialogs share one form that
 * outlives a tab switch; the page learns whether anything is dirty and can save
 * or discard it as one; leaving Settings or the page while dirty is guarded.
 */
export function SettingsSessions({ mods, children }: { mods: readonly Mod[]; children: ReactNode }) {
  const sessions = useRef(new Map<string, SettingsSession>());
  const [summary, setSummary] = useState({ dirty: false, saving: false });
  const [error, setError] = useState<string | null>(null);

  const report = useCallback((id: string, s: SettingsSession) => {
    sessions.current.set(id, s);
    const all = [...sessions.current.values()];
    const next = { dirty: all.some((x) => x.dirty), saving: all.some((x) => x.saving === true) };
    setSummary((prev) => (prev.dirty === next.dirty && prev.saving === next.saving ? prev : next));
  }, []);

  const saveAll = useCallback(async () => {
    setError(null);
    for (const id of [...sessions.current.keys()].sort()) {
      const s = sessions.current.get(id);
      if (!s?.dirty) continue;
      try {
        await s.save();
      } catch (e) {
        setError(message(e));
        throw e;
      }
    }
  }, []);

  const discardAll = useCallback(() => {
    setError(null);
    for (const s of sessions.current.values()) if (s.dirty) s.discard();
  }, []);

  const value = useMemo(() => ({ ...summary, error, saveAll, discardAll }), [summary, error, saveAll, discardAll]);

  const wrapped = mods.reduceRight<ReactNode>((inner, m) => {
    const S = m.settingsSession;
    return (
      <S.Provider>
        <Reporter id={m.id} useSession={S.useSession} report={report} />
        {inner}
      </S.Provider>
    );
  }, children);

  return (
    <SessionsContext.Provider value={value}>
      {wrapped}
      <UnsavedGuard />
    </SessionsContext.Provider>
  );
}

const inSettings = (pathname: string) => pathname === '/settings' || pathname.startsWith('/settings/');

/** beforeunload while dirty, and a prompt before an in-app navigation away from /settings. */
function UnsavedGuard() {
  const { dirty, saveAll, error } = useSessions();
  const blocker = useBlocker(({ nextLocation }) => dirty && !inSettings(nextLocation.pathname));
  useEffect(() => {
    if (!dirty) return undefined;
    const onUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', onUnload);
    return () => window.removeEventListener('beforeunload', onUnload);
  }, [dirty]);
  return (
    <Dialog open={blocker.state === 'blocked'} onClose={() => blocker.reset?.()}>
      <DialogTitle>Unsaved changes</DialogTitle>
      <DialogContent>
        <DialogContentText>Leaving Settings now discards the changes you have not saved.</DialogContentText>
        {error && (
          <Alert severity="error" sx={{ mt: 2 }}>
            {error}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={() => blocker.reset?.()}>Stay</Button>
        <Button color="warning" onClick={() => blocker.proceed?.()}>
          Leave without saving
        </Button>
        <Button
          variant="contained"
          onClick={() => {
            // A failed save leaves the prompt open with the reason; saveAll has shown it.
            saveAll().then(
              () => blocker.proceed?.(),
              () => {},
            );
          }}
        >
          Save and leave
        </Button>
      </DialogActions>
    </Dialog>
  );
}

/** The shell's own bar for a module whose session has no Footer: Save and Discard while dirty. */
export function DefaultSaveBar() {
  const { dirty, saving, error, saveAll, discardAll } = useSessions();
  if (!dirty) return null;
  return (
    <Paper variant="outlined" sx={{ p: 1.5, mt: 2 }}>
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
        {error && (
          <Alert severity="error" sx={{ flexGrow: 1 }}>
            {error}
          </Alert>
        )}
        <Box sx={{ flexGrow: 1 }} />
        <Button onClick={discardAll} disabled={saving}>
          Discard
        </Button>
        <Button variant="contained" onClick={() => void saveAll().catch(() => {})} disabled={saving}>
          Save settings
        </Button>
      </Stack>
    </Paper>
  );
}

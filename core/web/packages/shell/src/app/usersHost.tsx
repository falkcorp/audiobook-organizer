// file: core/web/packages/shell/src/app/usersHost.tsx
// version: 0.1.0
// guid: fc0d7211-29fe-4148-8998-18e77a6597ad
// last-edited: 2026-11-22

import { useCallback, useState } from 'react';
import { useGrants } from '../auth';
import type { UserPageUser, UserPanelProps, UserRowActionProps } from '../types';
import { useMountedModules } from './context';
import { LazyWithProps } from './LazySlot';

/**
 * The Users page's shared state for module slots (F9-03 prep): the page notice
 * and a refreshKey. A row action's onDone(message?) and a panel's onNotice(message)
 * both reload the list through `reload`, bump the key every panel reads, and show
 * the message as the page notice when there is one.
 */
export function useUserPageSession(reload: () => void) {
  const [refreshKey, setRefreshKey] = useState(0);
  const [notice, setNotice] = useState<string | null>(null);
  const onDone = useCallback(
    (message?: string) => {
      if (message) setNotice(message);
      setRefreshKey((k) => k + 1);
      reload();
    },
    [reload],
  );
  const onNotice = useCallback(
    (message: string) => {
      setNotice(message);
      setRefreshKey((k) => k + 1);
      reload();
    },
    [reload],
  );
  const dismissNotice = useCallback(() => setNotice(null), []);
  return { refreshKey, notice, onDone, onNotice, dismissNotice };
}

/** The rowActions the user may see, in module ID then declaration order. */
export function useUserRowActions() {
  const { modules } = useMountedModules();
  const grants = useGrants();
  return [...modules]
    .sort((a, b) => a.id.localeCompare(b.id))
    .flatMap((m) => (m.userPageExtensions?.rowActions ?? []).map((a) => ({ ...a, moduleId: m.id })))
    .filter((a) => grants(a.permission));
}

/** The panels the user may see, in module ID then `order`. */
export function useUserPanels() {
  const { modules } = useMountedModules();
  const grants = useGrants();
  return [...modules]
    .sort((a, b) => a.id.localeCompare(b.id))
    .flatMap((m) => (m.userPageExtensions?.panels ?? []).map((p) => ({ ...p, moduleId: m.id })))
    .filter((p) => grants(p.permission))
    .sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
}

/** Every visible row action of every module, for one row. */
export function UserRowActions({ user, users, onDone }: UserRowActionProps) {
  const actions = useUserRowActions();
  return (
    <>
      {actions.map((a) => (
        <LazyWithProps<UserRowActionProps> key={`${a.moduleId}.${a.id}`} load={a.lazy} label={`${a.moduleId} ${a.label}`} props={{ user, users, onDone }} />
      ))}
    </>
  );
}

export type { UserPageUser, UserPanelProps };

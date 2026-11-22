// file: core/web/packages/shell/src/app/shellPages.tsx
// version: 0.1.0
// guid: 5a9d2c74-0e63-4b18-8f47-d1b6e3a0c925
// last-edited: 2026-11-20

// The shell's signed-in pages (07 §7.4.3) as frames for module
// contributions. The pages ABO moves into the shell (Activity's tasks pane,
// Settings' generic tabs, System's status cards, Users' table) arrive with
// shell adoption (F9-04); until then each frame renders the contributions of
// the mounted modules and says when there are none.

import { useState } from 'react';
import { Alert, Box, List, ListItemButton, ListItemText, Paper, Stack, Tab, Table, TableBody, TableCell, TableHead, TableRow, Tabs, Typography } from '@mui/material';
import { Link as RouterLink, useParams } from 'react-router';
import { DataState } from '@mediacore/ui-kit';
import { useGrants } from '../auth';
import { SettingsSection } from '../settings';
import type { Lazy, UserPageUser, UserPanelProps } from '../types';
import { useMountedModules, useShellContext } from './context';
import { LazySlot, LazyWithProps } from './LazySlot';
import { DefaultSaveBar, SettingsSessions, sessionModules } from './settingsHost';
import { SetupWizard } from './setupWizard';
import { useUserPageSession, useUserPanels, UserRowActions } from './usersHost';
import { Page } from './Page';

// The shell's own pages, loaded lazily by the route table (07 §7.10, FE-18):
// /login with an empty cache must fetch none of them.

/** Module contributions visible to the user, in module ID then `order` order. */
function useSlots<T extends { id: string; permission?: string; order?: number }>(pick: (m: ReturnType<typeof useMountedModules>['modules'][number]) => readonly T[] | undefined) {
  const { modules, status } = useMountedModules();
  const grants = useGrants();
  const slots = modules
    .flatMap((m) => (pick(m) ?? []).map((s) => ({ ...s, moduleId: m.id })))
    .filter((s) => grants(s.permission))
    .sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
  return { slots, status };
}

function Widgets({ placement }: { placement: 'home' | 'system' | 'diagnostics' }) {
  const { slots, status } = useSlots((m) => (m.widgets ?? []).filter((w) => w.placement === placement));
  const st = status === 'ready' ? (slots.length ? 'ready' : 'empty') : status === 'error' ? 'error' : status === 'idle' ? 'empty' : 'loading';
  return (
    <DataState status={st} error="The module list did not load." emptyText="No module adds anything here.">
      <Stack spacing={2}>
        {slots.map((w) => (
          <Paper key={`${w.moduleId}.${w.id}`} variant="outlined" sx={{ p: 2 }}>
            <LazySlot load={w.lazy} label={`${w.moduleId} widget ${w.id}`} />
          </Paper>
        ))}
      </Stack>
    </DataState>
  );
}

export function HomePage() {
  const { displayName } = useShellContext();
  return (
    <Page title={displayName}>
      <Widgets placement="home" />
    </Page>
  );
}

function TabbedSlots({ slots, empty }: { slots: { id: string; label: string; lazy: Lazy; moduleId: string }[]; empty: string }) {
  const [tab, setTab] = useState(0);
  if (slots.length === 0) return <Typography color="text.secondary">{empty}</Typography>;
  const cur = slots[Math.min(tab, slots.length - 1)];
  return (
    <>
      <Tabs value={Math.min(tab, slots.length - 1)} onChange={(_, v: number) => setTab(v)} sx={{ mb: 2 }}>
        {slots.map((s) => (
          <Tab key={`${s.moduleId}.${s.id}`} label={s.label} />
        ))}
      </Tabs>
      <LazySlot key={`${cur.moduleId}.${cur.id}`} load={cur.lazy} label={cur.label} />
    </>
  );
}

export function ActivityPage() {
  const { slots } = useSlots((m) => m.activityPanes);
  return (
    <Page title="Activity">
      <TabbedSlots slots={slots} empty="No activity panes yet: the tasks, logs and audit panes arrive with the Activity page." />
    </Page>
  );
}

const NO_USERS: UserPageUser[] = [];
const noop = () => {};

export { SetupWizard };

export interface UsersPageProps {
  /** The account list. The table that loads it arrives with F9-03; until then the page has none. */
  users?: UserPageUser[];
  /** Reloads `users`: called when a row action or a panel reports a change. */
  reload?: () => void;
}

/**
 * /users: the module tabs, the account table with each module's row actions, and
 * each module's panels below it. A row action's onDone and a panel's onNotice show
 * the page notice, reload the list and bump the panels' refreshKey.
 */
export function UsersPage({ users = NO_USERS, reload = noop }: UsersPageProps) {
  const { slots } = useSlots((m) => m.userPageExtensions?.tabs);
  const panels = useUserPanels();
  const session = useUserPageSession(reload);
  return (
    <Page title="Users">
      {session.notice && (
        <Alert severity="info" onClose={session.dismissNotice} sx={{ mb: 2 }}>
          {session.notice}
        </Alert>
      )}
      {users.length > 0 && (
        <Table size="small" aria-label="Accounts" sx={{ mb: 3 }}>
          <TableHead>
            <TableRow>
              <TableCell>Username</TableCell>
              <TableCell>Status</TableCell>
              <TableCell>Roles</TableCell>
              <TableCell>Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {users.map((u) => (
              <TableRow key={u.id}>
                <TableCell>{u.username}</TableCell>
                <TableCell>{u.status}</TableCell>
                <TableCell>{(u.roles ?? []).join(', ')}</TableCell>
                <TableCell>
                  <UserRowActions user={u} users={users} onDone={session.onDone} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <TabbedSlots slots={slots} empty={panels.length > 0 ? '' : 'No user-page extensions yet: the user table arrives with the Users page.'} />
      {panels.map((p) => (
        <Box key={`${p.moduleId}.${p.id}`} sx={{ mt: 3 }}>
          <Typography variant="h6" component="h2" gutterBottom>
            {p.label}
          </Typography>
          <LazyWithProps<UserPanelProps> load={p.lazy} label={`${p.moduleId} ${p.label}`} props={{ users, refreshKey: session.refreshKey, onNotice: session.onNotice }} />
        </Box>
      ))}
    </Page>
  );
}

/**
 * /settings and /settings/<module>.<panel>: the panel list, then one panel (a module's
 * own, or the schema form). A module with a settingsSession shares one form across its
 * panels: its Provider wraps the page, its banners and dialogs render on every tab,
 * its footer (or the shell's Save / Discard bar) sits under the panel, and the page
 * guards leaving or unloading while the form is dirty.
 */
export function SettingsPage() {
  const { client } = useShellContext();
  const { modules } = useMountedModules();
  const { slots, status } = useSlots((m) => m.settingsPanels.map((p) => ({ ...p, lazy: p.lazy as Lazy | undefined })));
  const panelKey = useParams()['*'] ?? '';
  const cur = slots.find((p) => `${p.moduleId}.${p.id}` === panelKey);
  const mods = sessionModules(modules);
  const curMod = mods.find((m) => m.id === cur?.moduleId);
  return (
    <SettingsSessions mods={mods}>
      <Page title="Settings">
        {mods.map((m) => m.settingsSession.Banners && <m.settingsSession.Banners key={m.id} />)}
        <Stack direction="row" spacing={3} sx={{ alignItems: 'flex-start' }}>
          <List sx={{ minWidth: 200 }} aria-label="Settings panels">
            {slots.map((p) => (
              <ListItemButton key={`${p.moduleId}.${p.id}`} component={RouterLink} to={`/settings/${p.moduleId}.${p.id}`} selected={cur === p}>
                <ListItemText primary={p.label} secondary={p.moduleId} />
              </ListItemButton>
            ))}
          </List>
          <Box sx={{ flexGrow: 1 }}>
            {status === 'loading' && <DataState status="loading" />}
            {status !== 'loading' && slots.length === 0 && <Typography color="text.secondary">No settings panels.</Typography>}
            {cur?.lazy && <LazySlot key={panelKey} load={cur.lazy} label={cur.label} />}
            {cur && !cur.lazy && cur.schemaSection && <SettingsSection key={panelKey} client={client} section={cur.schemaSection} />}
            {!cur && slots.length > 0 && <Typography color="text.secondary">Choose a panel.</Typography>}
            {curMod?.settingsSession.Footer ? <curMod.settingsSession.Footer /> : curMod ? <DefaultSaveBar /> : null}
          </Box>
        </Stack>
        {mods.map((m) => m.settingsSession.Dialogs && <m.settingsSession.Dialogs key={m.id} />)}
      </Page>
    </SettingsSessions>
  );
}

/** /system: status widgets, diagnostics widgets, and System → UI diagnostics for admins (07 §7.5.2). */
export function SystemPage() {
  const { diagnostics } = useMountedModules();
  const grants = useGrants();
  const shown = diagnostics.filter((d) => d.level !== 'debug');
  return (
    <Page title="System">
      <Widgets placement="system" />
      <Typography variant="h6" component="h2" sx={{ mt: 3 }}>
        Diagnostics
      </Typography>
      <Widgets placement="diagnostics" />
      {grants('core:admin') && (
        <>
          <Typography variant="h6" component="h2" sx={{ mt: 3 }}>
            UI diagnostics
          </Typography>
          {shown.length === 0 ? (
            <Typography color="text.secondary">None on this page load.</Typography>
          ) : (
            <List dense data-testid="ui-diagnostics">
              {shown.map((d, i) => (
                <ListItemText key={i} primary={`${d.kind}${d.module ? ` · ${d.module}` : ''}${d.item ? ` · ${d.item}` : ''}`} secondary={d.detail} />
              ))}
            </List>
          )}
        </>
      )}
    </Page>
  );
}

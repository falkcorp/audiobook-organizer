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
import { Box, List, ListItemButton, ListItemText, Paper, Stack, Tab, Tabs, Typography } from '@mui/material';
import { Link as RouterLink, useParams } from 'react-router';
import { DataState } from '@mediacore/ui-kit';
import { useGrants } from '../auth';
import { SettingsSection } from '../settings';
import type { Lazy } from '../types';
import { useMountedModules, useShellContext } from './context';
import { LazySlot } from './MainLayout';
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

export function UsersPage() {
  const { slots } = useSlots((m) => m.userPageExtensions?.tabs);
  return (
    <Page title="Users">
      <TabbedSlots slots={slots} empty="No user-page extensions yet: the user table arrives with the Users page." />
    </Page>
  );
}

/** /settings and /settings/<module>.<panel>: the panel list, then one panel (a module's own, or the schema form). */
export function SettingsPage() {
  const { client } = useShellContext();
  const { slots, status } = useSlots((m) => m.settingsPanels.map((p) => ({ ...p, lazy: p.lazy as Lazy | undefined })));
  const panelKey = useParams()['*'] ?? '';
  const cur = slots.find((p) => `${p.moduleId}.${p.id}` === panelKey);
  return (
    <Page title="Settings">
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
        </Box>
      </Stack>
    </Page>
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

// file: core/web/packages/shell/src/settings/SettingsSection.tsx
// version: 0.1.0
// guid: 6058aee3-231b-42e3-82ce-82fb12bf2fe5
// last-edited: 2026-11-20

import { DataState } from '@mediacore/ui-kit';
import { useEffect, useState } from 'react';
import type { KeyStateView, SettingsSchemaDoc, ShellClient } from '../api/interim';
import { errorMessage, isAbortError, unwrap } from '../api/problems';
import { SchemaForm } from './SchemaForm';

export interface SettingsSectionProps {
  client: ShellClient;
  /** The config section path, e.g. "modules.batcave". */
  section: string;
}

type State =
  | { phase: 'loading' }
  | { phase: 'error'; message: string }
  | { phase: 'ready'; section: string; schema: SettingsSchemaDoc; keys: KeyStateView[] };

/**
 * Loads GET /settings/schema and GET /settings together and renders the section's
 * SchemaForm: loading, error (with retry), and the form (which has its own empty
 * state for a section with no keys). Both requests abort on unmount. After a save only the data is refetched,
 * so the form stays on screen.
 */
export function SettingsSection({ client, section }: SettingsSectionProps) {
  const [state, setState] = useState<State>({ phase: 'loading' });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    Promise.all([unwrap(client.GET('/api/core/v1/settings/schema', { signal })), unwrap(client.GET('/api/core/v1/settings', { signal }))])
      .then(([schema, settings]) => setState({ phase: 'ready', section, schema, keys: Object.values(settings) }))
      .catch((err: unknown) => {
        if (isAbortError(err) || signal.aborted) return;
        setState({ phase: 'error', message: errorMessage(err) });
      });
    return () => controller.abort();
  }, [client, section, attempt]);

  // Data of another section is stale: show loading until this one's arrives.
  const current = state.phase === 'ready' && state.section !== section ? { phase: 'loading' as const } : state;

  return (
    <DataState
      status={current.phase === 'ready' ? 'ready' : current.phase}
      error={current.phase === 'error' ? current.message : undefined}
      onRetry={() => {
        setState({ phase: 'loading' });
        setAttempt((n) => n + 1);
      }}
    >
      {current.phase === 'ready' && (
        <SchemaForm
          client={client}
          section={section}
          schema={current.schema.properties[section]}
          keys={current.keys}
          onSaved={() => setAttempt((n) => n + 1)}
        />
      )}
    </DataState>
  );
}

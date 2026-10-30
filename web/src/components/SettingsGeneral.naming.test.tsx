// file: web/src/components/SettingsGeneral.naming.test.tsx
// version: 1.0.0
// guid: 3b8f0c52-6d1e-4a97-8c43-e25a9d7b1f60
// last-edited: 2026-10-30

import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { SettingsGeneral } from './SettingsGeneral';
import type { SettingsState } from '../pages/Settings';

function renderGeneral(folderPattern: string, tokens?: string[]) {
  const settings = {
    folderNamingPattern: folderPattern,
    fileNamingPattern: '{title}',
    supportedExtensions: [],
    excludePatterns: [],
  } as unknown as SettingsState;
  const noop = vi.fn();
  return render(
    <SettingsGeneral
      settings={settings}
      setSettings={noop}
      libraryPathError={null}
      handleChange={noop}
      handleBrowseLibraryPath={noop}
      extensionsInput=""
      setExtensionsInput={noop}
      extensionsError={null}
      handleAddExtension={noop}
      handleRemoveExtension={noop}
      excludePatternInput=""
      setExcludePatternInput={noop}
      excludePatternError={null}
      handleAddExcludePattern={noop}
      handleRemoveExcludePattern={noop}
      backupNotice={null}
      createBackupInProgress={false}
      handleCreateBackup={noop}
      backupsLoading={false}
      backups={[]}
      handleRequestRestore={noop}
      handleRequestDeleteBackup={noop}
      namingPatternTokens={tokens}
    />
  );
}

describe('SettingsGeneral naming-pattern tokens', () => {
  it('renders the server-served token list in the help text', () => {
    renderGeneral('{title}', ['{title}', '{joker}', '{series_prefix}']);
    expect(screen.getByText(/Available: \{title\}, \{joker\}, \{series_prefix\}\./)).toBeTruthy();
  });

  it('falls back to the static list while the server list is not loaded', () => {
    renderGeneral('{title}');
    expect(screen.getByText(/Available: .*\{series_prefix\}/)).toBeTruthy();
    expect(screen.queryByText(/\{track_number\}/)).toBeNull();
  });

  it('warns inline about a token the server does not list', () => {
    renderGeneral('{author}/{title} {batmobile}', ['{author}', '{title}']);
    const warning = screen.getByTestId('unknown-pattern-tokens');
    expect(warning.textContent).toContain('{batmobile}');
  });

  it('shows no warning when every token is known', () => {
    renderGeneral('{author}/{title}', ['{author}', '{title}']);
    expect(screen.queryByTestId('unknown-pattern-tokens')).toBeNull();
  });
});

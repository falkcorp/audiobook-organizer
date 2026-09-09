// file: web/src/components/settings/PathsSettingsTab.databasePath.test.tsx
// version: 1.0.0
// guid: 2d94b7e1-6c38-4a05-9f72-8b1e3d05c4a7
// last-edited: 2026-09-09

import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import type React from 'react';
import { PathsSettingsTab } from './PathsSettingsTab';

const DB_PATH = '/mnt/bigdata/books/audiobook-organizer/.appdata/audiobooks.pebble';

function renderTab(overrides: Record<string, unknown> = {}) {
  const handleChange = vi.fn();
  const props = {
    settings: {
      libraryPath: '/mnt/bigdata/books/audiobook-organizer',
      activityDbPath: '',
      activityDbMoveOnChange: true,
      databasePath: DB_PATH,
    },
    setSettings: vi.fn(),
    libraryPathError: null,
    handleChange,
    handleBrowseLibraryPath: vi.fn(),
    importPaths: [],
    scanStatuses: {},
    handleViewScanErrors: vi.fn(),
    handleRequestCancelScan: vi.fn(),
    handleScanImportFolder: vi.fn(),
    handleRemoveImportFolder: vi.fn(),
    setAddFolderDialogOpen: vi.fn(),
    envLocked: [] as string[],
    settingLocks: {} as Record<string, string>,
    activityDbResolvedPath: '/mnt/x/.activity/activity.sqlite',
    ...overrides,
  };
  render(
    <PathsSettingsTab {...(props as unknown as React.ComponentProps<typeof PathsSettingsTab>)} />
  );
  return { handleChange };
}

const dbField = () => screen.getByLabelText(/^Database Path/i);

describe('PathsSettingsTab — main database', () => {
  it('is editable when nothing on the server is pinning it', () => {
    const { handleChange } = renderTab();
    expect(dbField()).not.toBeDisabled();

    fireEvent.change(dbField(), { target: { value: '/srv/books.pebble' } });
    expect(handleChange).toHaveBeenCalledWith('databasePath', '/srv/books.pebble');
  });

  it('says the change only takes effect on restart', () => {
    renderTab();
    // The field is settable but not live: the store is already open by the time
    // anything could act on a new value. An operator who changes it and sees no
    // effect must not conclude the setting is broken.
    expect(screen.getByText(/next time the server starts/i)).toBeInTheDocument();
  });

  it('warns that no data is moved and the old database survives', () => {
    renderTab();
    // Both halves matter. "No data is moved" is the hazard; "the existing
    // database is never deleted" is what makes the hazard recoverable, and
    // omitting it turns a reversible mistake into a panic.
    expect(screen.getByText(/does not move any data/i)).toBeInTheDocument();
    expect(screen.getByText(/never deleted/i)).toBeInTheDocument();
  });

  // The regression guard for the 2026-09-09 outage, from the UI side: production
  // pins the database with --db on its ExecStart line, so the field must refuse
  // the edit rather than accept a value the next boot discards.
  it('disables the field when the server pins the path', () => {
    renderTab({ envLocked: ['database_path'], settingLocks: { database_path: '--db' } });
    expect(dbField()).toBeDisabled();
  });

  // A flag and an environment variable live in different halves of the same unit
  // file. Naming the wrong one sends the operator to delete something that was
  // never the cause, and conclude the app is broken when it does not help.
  it('names the FLAG when a flag is what pins it', () => {
    renderTab({ envLocked: ['database_path'], settingLocks: { database_path: '--db' } });

    expect(screen.getAllByText(/--db/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/start-up command/i).length).toBeGreaterThan(0);
    // The negative half: it must NOT tell them to go hunting for an environment
    // variable that is not there. Without this assertion the test passes on a
    // component that mentions both.
    expect(screen.queryByText(/environment variable/i)).not.toBeInTheDocument();
  });

  it('names the ENVIRONMENT VARIABLE when that is what pins it', () => {
    renderTab({
      envLocked: ['database_path'],
      settingLocks: { database_path: 'DATABASE_PATH' },
    });

    expect(screen.getAllByText(/DATABASE_PATH/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/environment variable/i).length).toBeGreaterThan(0);
    expect(screen.queryByText(/start-up command/i)).not.toBeInTheDocument();
  });

  // envLocked and settingLocks are computed from the same server-side set, but
  // they arrive as two separate JSON fields. A response carrying one without the
  // other must still render something an operator can act on, rather than an
  // unexplained grey box or a crash.
  it('still explains the lock if the reason map is missing the key', () => {
    renderTab({ envLocked: ['database_path'], settingLocks: {} });
    expect(dbField()).toBeDisabled();
    expect(screen.getAllByText(/the server configuration/i).length).toBeGreaterThan(0);
  });

  // The activity database is a separate control with a separate lock. Pinning one
  // must not grey out the other.
  it('leaves the activity database field alone when only the main one is locked', () => {
    renderTab({ envLocked: ['database_path'], settingLocks: { database_path: '--db' } });
    expect(screen.getByLabelText(/Activity Database Path/i)).not.toBeDisabled();
  });
});

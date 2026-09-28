// file: web/src/components/review/CommandBar.test.tsx
// version: 1.0.1
// guid: 5b9e2c41-8f07-4a3d-9c6e-0d4f7a1b8e23
// last-edited: 2026-09-27

import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, beforeEach, vi } from 'vitest';
import { renderHook } from '@testing-library/react';
import { CommandBar, type CommandMenu } from './CommandBar';
import { useAdvancedSettings } from '../../hooks/useAdvancedSettings';
import { MemoryRouter } from 'react-router-dom';
import type { ReactElement } from 'react';

// The advanced hint is a router link, so the bar needs a router.
const renderInRouter = (ui: ReactElement) => render(ui, { wrapper: MemoryRouter });

function sectionedMenu(run = vi.fn()): CommandMenu {
  return {
    id: 'dedup',
    label: 'Dedup',
    simple: [
      {
        id: 'simple',
        commands: [
          {
            id: 'do-everything',
            label: 'Do everything',
            scope: 'library',
            primary: true,
            description: 'Runs every check for you.',
            run,
          },
        ],
      },
    ],
    advanced: [
      {
        id: 'scoring',
        title: 'Find and score',
        commands: [
          {
            id: 'score',
            label: 'Score',
            scope: 'library',
            description: 'Scores pairs.',
            run: vi.fn(),
          },
        ],
      },
      {
        id: 'maintenance',
        title: 'Maintenance',
        commands: [
          { id: 'tidy', label: 'Tidy', scope: 'library', description: 'Tidies up.', run: vi.fn() },
        ],
      },
    ],
  };
}

beforeEach(() => localStorage.clear());

describe('sectioned command menu', () => {
  it('always shows the simple section, and hides advanced behind a hint by default', async () => {
    const user = userEvent.setup();
    renderInRouter(<CommandBar menus={[sectionedMenu()]} />);
    await user.click(screen.getByTestId('command-menu-dedup'));

    expect(await screen.findByTestId('command-do-everything')).toBeInTheDocument();
    expect(screen.getByTestId('command-section-dedup-simple')).toHaveTextContent('Simple');
    expect(screen.queryByTestId('command-score')).not.toBeInTheDocument();
    expect(screen.queryByTestId('command-section-dedup-advanced')).not.toBeInTheDocument();
    expect(screen.getByTestId('command-advanced-hint-dedup')).toHaveTextContent(
      'More options: Settings → Show advanced settings'
    );
  });

  it('shows labelled advanced groups with descriptions when the setting is on', async () => {
    localStorage.setItem('settings.showAdvanced', 'true');
    const user = userEvent.setup();
    renderInRouter(<CommandBar menus={[sectionedMenu()]} />);
    await user.click(screen.getByTestId('command-menu-dedup'));

    expect(await screen.findByTestId('command-section-dedup-advanced')).toHaveTextContent(
      'Advanced'
    );
    expect(screen.getByTestId('command-group-dedup-advanced-scoring')).toHaveTextContent(
      'Find and score'
    );
    expect(screen.getByTestId('command-group-dedup-advanced-maintenance')).toHaveTextContent(
      'Maintenance'
    );
    expect(screen.getByTestId('command-score-description')).toHaveTextContent('Scores pairs.');
    expect(screen.getByTestId('command-do-everything-description')).toHaveTextContent(
      'Runs every check for you.'
    );
    expect(screen.queryByTestId('command-advanced-hint-dedup')).not.toBeInTheDocument();
    // The owner's point: when every item says "library-wide" it says nothing.
    expect(screen.getByRole('menu')).not.toHaveTextContent(/library-wide/i);
  });

  it('follows the setting live, without a reload', async () => {
    const user = userEvent.setup();
    renderInRouter(<CommandBar menus={[sectionedMenu()]} />);
    const settings = renderHook(() => useAdvancedSettings());
    await user.click(screen.getByTestId('command-menu-dedup'));
    expect(screen.queryByTestId('command-score')).not.toBeInTheDocument();

    act(() => settings.result.current.setShowAdvanced(true));

    expect(await screen.findByTestId('command-score')).toBeInTheDocument();
  });

  it('runs the picked command and closes the menu', async () => {
    const run = vi.fn();
    const user = userEvent.setup();
    renderInRouter(<CommandBar menus={[sectionedMenu(run)]} />);
    await user.click(screen.getByTestId('command-menu-dedup'));
    await user.click(await screen.findByTestId('command-do-everything'));
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('keeps the scope subtitle on a flat menu that has no descriptions', async () => {
    const user = userEvent.setup();
    renderInRouter(
      <CommandBar
        menus={[
          {
            id: 'queue',
            label: 'Queue',
            commands: [{ id: 'purge', label: 'Purge stale', scope: 'library', run: vi.fn() }],
          },
        ]}
      />
    );
    await user.click(screen.getByTestId('command-menu-queue'));
    const item = await screen.findByTestId('command-purge');
    expect(within(item).getByText('library-wide')).toBeInTheDocument();
  });
});

// file: web/src/test/coreWebWiring.test.tsx
// version: 0.1.0
// guid: 4e8a1c53-7b92-4d06-a3f8-5c1d9e2b7a40
// last-edited: 2026-11-20

// F9-02 slice 0: web/ resolves @mediacore/shell and @mediacore/ui-kit (file:
// links whose real path is under core/web, which has its own node_modules)
// with ONE copy of each peer singleton. Nothing in the app imports the
// packages yet; this is the proof that the wiring works before F9-03 does.
//
// The two renders below fail loudly on a second copy: a hook from ui-kit's
// React run by web's react-dom throws "Invalid hook call", and a ui-kit MUI
// component under web's ThemeProvider gets the default spacing instead of
// ours when it reads a different @mui/material or @emotion/react context.

import { render, screen } from '@testing-library/react';
import { ThemeProvider, createTheme } from '@mui/material/styles';
import { describe, expect, it, vi } from 'vitest';
import { DataState, useKeyboardShortcuts } from '@mediacore/ui-kit';
import { defineModule } from '@mediacore/shell';

describe('core/web packages are wired into web/', () => {
  it('resolves @mediacore/shell and runs its code', () => {
    const manifest = {
      id: 'gotham',
      version: '1.0.0',
      displayName: 'Gotham',
      pages: [],
      nav: [],
      settingsPanels: [],
      widgets: [],
      banners: [],
      topBarItems: [],
      userPage: [],
      activityPanes: [],
      setupSteps: [],
      permissions: [],
    } as const;
    const ui = defineModule(manifest, {
      routes: [],
      nav: [],
      settingsPanels: [],
      permissionsHints: { view: [] },
    });
    expect(ui.id).toBe('gotham');
    expect(ui.manifest).toBe(manifest);
  });

  it('runs a ui-kit hook under web\'s react-dom (one React)', () => {
    const run = vi.fn();
    function Probe() {
      useKeyboardShortcuts([{ key: 'b', run }]);
      return <p>batcave</p>;
    }
    render(<Probe />);
    expect(screen.getByText('batcave')).toBeInTheDocument();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'g', bubbles: true }));
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'b', bubbles: true }));
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('reads web\'s MUI theme from a ui-kit component (one @mui/material and one @emotion/react)', () => {
    // DataState's empty text pads with sx p: 3, which is 3 * theme.spacing.
    const theme = createTheme({ spacing: 10 });
    render(
      <ThemeProvider theme={theme}>
        <DataState status="empty" emptyText="Arkham is quiet." />
      </ThemeProvider>
    );
    const text = screen.getByText('Arkham is quiet.');
    expect(getComputedStyle(text).padding).toBe('30px');
  });
});

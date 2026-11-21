// file: core/web/packages/shell/test/layout/render.tsx
// version: 0.1.0
// guid: c4d7a2e9-6f13-4b58-9e0a-d1b5c8f3a627
// last-edited: 2026-11-21

import type { ReactElement, ReactNode } from 'react';
import { render, type RenderOptions } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { createTheme, ThemeProvider } from '@mui/material';

interface Options extends Omit<RenderOptions, 'wrapper'> {
  /** Initial URL entries for MemoryRouter (default: ['/']). */
  initialEntries?: string[];
}

/** The layout components' tests: a MemoryRouter and MUI's default theme, nothing of any app's. */
export function renderWithProviders(ui: ReactElement, { initialEntries = ['/'], ...options }: Options = {}) {
  const theme = createTheme();
  function Wrapper({ children }: { children: ReactNode }) {
    return (
      <MemoryRouter initialEntries={initialEntries}>
        <ThemeProvider theme={theme}>{children}</ThemeProvider>
      </MemoryRouter>
    );
  }
  return render(ui, { wrapper: Wrapper, ...options });
}

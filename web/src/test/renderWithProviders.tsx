// file: web/src/test/renderWithProviders.tsx
// version: 1.1.1
// last-edited: 2026-08-06

import React from 'react';
import { render, type RenderOptions } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { ThemeProvider } from '@mui/material';
import { createAppTheme } from '../theme';

const testTheme = createAppTheme('dark');

interface ProviderOptions extends Omit<RenderOptions, 'wrapper'> {
  /** Initial URL entries for MemoryRouter (default: ['/']) */
  initialEntries?: string[];
}

/**
 * Custom render that wraps components in the providers they need:
 * MemoryRouter (for Link/navigate with configurable routes) and ThemeProvider (for MUI).
 */
export function renderWithProviders(
  ui: React.ReactElement,
  options: ProviderOptions = {}
) {
  const { initialEntries = ['/'], ...renderOptions } = options;

  function Wrapper({ children }: { children: React.ReactNode }) {
    return (
      <MemoryRouter initialEntries={initialEntries}>
        <ThemeProvider theme={testTheme}>{children}</ThemeProvider>
      </MemoryRouter>
    );
  }

  return render(ui, { wrapper: Wrapper, ...renderOptions });
}

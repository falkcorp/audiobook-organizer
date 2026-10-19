// file: web/src/App.test.tsx
// version: 1.0.12
// guid: 9a0b1c2d-3e4f-5a6b-7c8d-9e0f1a2b3c4d
// last-edited: 2026-10-19

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { BrowserRouter } from 'react-router-dom';
import { ThemeProvider } from '@mui/material';
import App from './App';
import { appTheme } from './theme';
import { AuthProvider } from './contexts/AuthContext';

const theme = appTheme;

// Mock API
vi.mock('./services/api', async () => (await import('./test/appApiMock')).createAppApiMock());

beforeEach(() => {
  vi.clearAllMocks();
});

describe('App', () => {
  it('renders without crashing', async () => {
    render(
      <BrowserRouter>
        <ThemeProvider theme={theme}>
          <AuthProvider>
            <App />
          </AuthProvider>
        </ThemeProvider>
      </BrowserRouter>
    );

    expect(await screen.findByText('Audiobook Organizer')).toBeInTheDocument();
  });

  it('renders navigation items', async () => {
    render(
      <BrowserRouter>
        <ThemeProvider theme={theme}>
          <AuthProvider>
            <App />
          </AuthProvider>
        </ThemeProvider>
      </BrowserRouter>
    );

    expect((await screen.findAllByText('Dashboard')).length).toBeGreaterThan(0);
    expect((await screen.findAllByText('Library')).length).toBeGreaterThan(0);
  });
});

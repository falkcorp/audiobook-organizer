// file: web/src/components/tools/ToolsPanel.test.tsx
// version: 1.0.0
// guid: f8a9b0c1-d2e3-4567-fabc-567890123455
// last-edited: 2026-06-15

import { render, screen } from '@testing-library/react';
import { ToolsPanel } from './ToolsPanel';
import { vi } from 'vitest';

vi.mock('../../services/api', () => ({
  getTools: vi.fn().mockResolvedValue([
    { name: 'ollama', mode: 'system', available: true, resolved_path: '/usr/bin/ollama', version: '0.30.8' },
    { name: 'fpcalc', mode: 'disabled', available: false },
  ]),
  installTool: vi.fn(),
}));

test('renders tool cards', async () => {
  render(<ToolsPanel mode="settings" />);
  expect(await screen.findByText('ollama')).toBeInTheDocument();
  expect(await screen.findByText('fpcalc')).toBeInTheDocument();
});

test('shows available chip for available tools', async () => {
  render(<ToolsPanel mode="settings" />);
  expect(await screen.findByText('Available')).toBeInTheDocument();
});

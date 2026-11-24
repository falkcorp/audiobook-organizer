// file: web/src/components/wizard/wizardSignInRequired.test.tsx
// version: 0.1.0
// guid: d3642724-31d4-4ea6-b5a1-92a4e4b01435
// last-edited: 2026-11-22

// SEC5-8 in the first-run wizard: under auth-off core refuses POST /tools/:name/install, so the
// Tools step says "Turn on sign-in to do this" and Complete Setup saves the setup but installs
// nothing and tells the user why; a refusal that arrives with the setting unknown maps to the
// same message instead of a raw 403.

import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { WelcomeWizard } from './WelcomeWizard';
import * as api from '../../services/api';

const toastSpy = vi.hoisted(() => vi.fn());
const authState = vi.hoisted(() => ({
  value: null as { initialized: boolean; authEnabled: boolean | null } | null,
}));
vi.mock('../../contexts/AuthContext', () => ({ useOptionalAuth: () => authState.value }));
vi.mock('../toast/ToastProvider', () => ({
  useToast: () => ({ toast: toastSpy }),
  ToastProvider: ({ children }: { children: React.ReactNode }) => children,
}));
vi.mock('../tools/ToolsPanel', () => ({ ToolsPanel: () => <div data-testid="tools-panel" /> }));
vi.mock('../common/ServerFileBrowser', () => ({ ServerFileBrowser: () => null }));
vi.mock('../../services/api', async () => ({
  ...(await vi.importActual<typeof api>('../../services/api')),
  getAppVersion: vi.fn(),
  getHomeDirectory: vi.fn(),
  updateConfig: vi.fn(),
  addImportPath: vi.fn(),
  installTool: vi.fn(),
}));

const NOTICE = /Turn on sign-in to do this/;
const next = () => screen.getByRole('button', { name: /^next$/i });

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  vi.mocked(api.getAppVersion).mockResolvedValue('0.0.0-test');
  vi.mocked(api.getHomeDirectory).mockResolvedValue('/home/tester');
  vi.mocked(api.updateConfig).mockResolvedValue({} as never);
  vi.mocked(api.installTool).mockResolvedValue({ path: '/tools/x', version: '1' });
});
afterEach(() => localStorage.clear());

async function toToolsStep() {
  const onComplete = vi.fn();
  render(<WelcomeWizard open onComplete={onComplete} />);
  const field = await screen.findByLabelText('Library Path');
  await waitFor(() => expect(field).toHaveValue('/home/tester'));
  fireEvent.click(next());
  await screen.findByText('Set up AI & fingerprinting tools');
  return onComplete;
}
async function finish() {
  fireEvent.click(next());
  await screen.findByLabelText(/OpenAI API Key/i);
  fireEvent.click(next());
  await screen.findByText(/iTunes/i, { selector: 'h6' });
  fireEvent.click(next());
  await screen.findByText('Add Import Folders');
  await userEvent.setup().click(screen.getByRole('button', { name: 'Complete Setup' }));
}

describe('wizard under auth-off', () => {
  it('shows the notice on the Tools and Import Folders steps, saves the setup, installs nothing', async () => {
    authState.value = { initialized: true, authEnabled: false };
    const onComplete = await toToolsStep();
    expect(screen.getByText(NOTICE)).toBeInTheDocument();
    await finish();
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.updateConfig)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.installTool)).not.toHaveBeenCalled();
    await waitFor(() => expect(toastSpy).toHaveBeenCalledWith(expect.stringMatching(NOTICE), 'warning'));
  });

  it('with auth on there is no notice and the install runs', async () => {
    authState.value = { initialized: true, authEnabled: true };
    const onComplete = await toToolsStep();
    expect(screen.queryByText(NOTICE)).toBeNull();
    await finish();
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(vi.mocked(api.installTool)).toHaveBeenCalledTimes(2));
  });

  it('maps an interactive_required refusal with the setting unknown to the message, not a raw 403', async () => {
    authState.value = { initialized: true, authEnabled: null };
    vi.mocked(api.installTool).mockRejectedValue(new api.ApiError('raw 403 text', 403, { code: 'interactive_required' }));
    const onComplete = await toToolsStep();
    await finish();
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(toastSpy).toHaveBeenCalledWith(expect.stringMatching(NOTICE), 'warning'));
    expect(toastSpy.mock.calls.some((c) => /raw 403/.test(String(c[0])))).toBe(false);
  });
});

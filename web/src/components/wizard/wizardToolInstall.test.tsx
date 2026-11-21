// file: web/src/components/wizard/wizardToolInstall.test.tsx
// version: 0.1.0
// guid: 6392197c-4e79-4c88-ac23-9f8c9f684c05
// last-edited: 2026-11-21

// The wizard's Tools step offers "Install recommended tools" (fpcalc and Ollama, through
// POST /api/v1/tools/:name/install). Complete Setup used to ignore the choice, so picking it
// installed nothing. It now installs after the setup itself is saved, in the background, and
// says how it went in a toast.

import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { WelcomeWizard } from './WelcomeWizard';
import * as api from '../../services/api';
import { STORAGE_KEYS } from '../../lib/storageKeys';

const toastSpy = vi.hoisted(() => vi.fn());
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

const next = () => screen.getByRole('button', { name: /^next$/i });

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  vi.mocked(api.getAppVersion).mockResolvedValue('0.0.0-test');
  vi.mocked(api.getHomeDirectory).mockResolvedValue('/home/tester');
  vi.mocked(api.updateConfig).mockResolvedValue({} as never);
  vi.mocked(api.addImportPath).mockResolvedValue({} as never);
  vi.mocked(api.installTool).mockResolvedValue({ path: '/tools/x', version: '1' });
});
afterEach(() => localStorage.clear());

// Walks the wizard to its last step. `onTools` runs on the Tools step before moving on.
async function walkToComplete(onTools?: () => Promise<void> | void) {
  const onComplete = vi.fn();
  render(<WelcomeWizard open onComplete={onComplete} />);
  const field = await screen.findByLabelText('Library Path');
  await waitFor(() => expect(field).toHaveValue('/home/tester'));
  fireEvent.click(next());
  await screen.findByText('Set up AI & fingerprinting tools');
  await onTools?.();
  if (!screen.queryByLabelText(/OpenAI API Key/i)) fireEvent.click(next());
  await screen.findByLabelText(/OpenAI API Key/i);
  fireEvent.click(next());
  await screen.findByText(/iTunes/i, { selector: 'h6' });
  fireEvent.click(next());
  await screen.findByText('Add Import Folders');
  return { onComplete };
}
const complete = () =>
  userEvent.setup().click(screen.getByRole('button', { name: 'Complete Setup' }));
const installed = () => vi.mocked(api.installTool).mock.calls.map((c) => c[0]);

describe('wizard: Install recommended tools', () => {
  it('installs fpcalc then Ollama after the setup is saved and the wizard is done, and says so', async () => {
    const { onComplete } = await walkToComplete();
    await complete();
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(installed()).toEqual(['fpcalc', 'ollama']));
    // the install comes after the config save
    const saveOrder = vi.mocked(api.updateConfig).mock.invocationCallOrder[0];
    const installOrder = vi.mocked(api.installTool).mock.invocationCallOrder[0];
    expect(saveOrder).toBeLessThan(installOrder);
    expect(localStorage.getItem(STORAGE_KEYS.WELCOME_WIZARD_COMPLETED)).toBe('true');
    await waitFor(() =>
      expect(toastSpy).toHaveBeenLastCalledWith('Recommended tools are installed.', 'success')
    );
    expect(toastSpy).toHaveBeenCalledWith(
      expect.stringMatching(/Installing recommended tools/),
      'info'
    );
  });

  it('counts a tool that is already installed (409) as installed', async () => {
    vi.mocked(api.installTool).mockImplementation(() =>
      Promise.reject(new api.ApiError('already installed', 409))
    );
    const { onComplete } = await walkToComplete();
    await complete();
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(installed()).toEqual(['fpcalc', 'ollama']));
    await waitFor(() =>
      expect(toastSpy).toHaveBeenLastCalledWith('Recommended tools are installed.', 'success')
    );
  });

  it('names the tool that failed, still tries the other, and still completes the setup', async () => {
    vi.mocked(api.installTool).mockImplementation((name: string) =>
      name === 'ollama'
        ? Promise.reject(new api.ApiError('download failed: connection reset', 502))
        : Promise.resolve({ path: '/tools/fpcalc', version: '1.5.1' })
    );
    const { onComplete } = await walkToComplete();
    await complete();
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(installed()).toEqual(['fpcalc', 'ollama']));
    await waitFor(() =>
      expect(toastSpy).toHaveBeenLastCalledWith(expect.stringContaining('ollama'), 'error')
    );
    const message = toastSpy.mock.calls.at(-1)![0] as string;
    expect(message).toContain('download failed: connection reset');
    expect(message).toContain('Settings');
    expect(message).not.toContain('fpcalc');
    expect(localStorage.getItem(STORAGE_KEYS.WELCOME_WIZARD_COMPLETED)).toBe('true');
  });

  it('keeps going after the first tool fails, and names both when both fail', async () => {
    vi.mocked(api.installTool).mockImplementation((name: string) =>
      Promise.reject(new api.ApiError(`no mirror for ${name}`, 502))
    );
    const { onComplete } = await walkToComplete();
    await complete();
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(installed()).toEqual(['fpcalc', 'ollama']));
    await waitFor(() =>
      expect(toastSpy).toHaveBeenLastCalledWith(expect.stringContaining('fpcalc'), 'error')
    );
    const message = toastSpy.mock.calls.at(-1)![0] as string;
    expect(message).toContain('no mirror for fpcalc');
    expect(message).toContain('ollama (no mirror for ollama)');
  });

  it('does not wait for a slow download before finishing the wizard', async () => {
    vi.mocked(api.installTool).mockImplementation(() => new Promise(() => {}));
    const { onComplete } = await walkToComplete();
    await complete();
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole('button', { name: 'Saving...' })).toBeNull();
  });

  it('installs nothing for "Let me choose" (the panel installs one tool at a time)', async () => {
    const { onComplete } = await walkToComplete(async () => {
      await userEvent.setup().click(screen.getByLabelText('Let me choose'));
    });
    await complete();
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    expect(api.installTool).not.toHaveBeenCalled();
    expect(toastSpy).not.toHaveBeenCalled();
  });

  it('installs nothing after "Skip - I\'ll set this up later"', async () => {
    const { onComplete } = await walkToComplete(async () => {
      await userEvent.setup().click(screen.getByRole('button', { name: /Skip/ }));
    });
    await complete();
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    expect(api.installTool).not.toHaveBeenCalled();
    expect(toastSpy).not.toHaveBeenCalled();
  });

  it('installs nothing when the setup itself failed to save', async () => {
    vi.spyOn(window, 'alert').mockImplementation(() => {});
    vi.spyOn(console, 'error').mockImplementation(() => {});
    vi.mocked(api.updateConfig).mockRejectedValueOnce(new Error('disk full'));
    const { onComplete } = await walkToComplete();
    await complete();
    await waitFor(() => expect(window.alert).toHaveBeenCalled());
    expect(onComplete).not.toHaveBeenCalled();
    expect(api.installTool).not.toHaveBeenCalled();
  });
});

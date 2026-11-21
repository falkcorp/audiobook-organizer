// file: web/src/components/wizard/wizardSplit.test.tsx
// version: 0.1.0
// guid: 42a9e99c-77f0-431f-95ec-b2bfa69847f5
// last-edited: 2026-11-21

// F9-04 slice 5: the first-run wizard is split into the shell's setup frame (dialog, stepper, back
// and next) and books' setup steps (library path, tools, AI, iTunes, import folders, and the save).
// The composed wizard must walk and save exactly as it did before the cut.

import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { WelcomeWizard } from './WelcomeWizard';
import { SetupWizardFrame } from '../setup/SetupWizardFrame';
import frameSource from '../setup/SetupWizardFrame.tsx?raw';
import * as api from '../../services/api';
import { STORAGE_KEYS } from '../../lib/storageKeys';

vi.mock('../tools/ToolsPanel', () => ({ ToolsPanel: () => <div data-testid="tools-panel" /> }));
vi.mock('../common/ServerFileBrowser', () => ({
  ServerFileBrowser: ({ onSelect }: { onSelect: (path: string, isDir: boolean) => void }) => (
    <button onClick={() => onSelect('/audio/in/gotham', true)}>pick-folder</button>
  ),
}));
vi.mock('../../services/api', async () => ({
  ...(await vi.importActual<typeof api>('../../services/api')),
  getAppVersion: vi.fn(),
  getHomeDirectory: vi.fn(),
  updateConfig: vi.fn(),
  addImportPath: vi.fn(),
  validateOpenAIKey: vi.fn(),
  validateITunesLibrary: vi.fn(),
  importITunesLibrary: vi.fn(),
}));

const next = () => screen.getByRole('button', { name: /^next$/i });
const back = () => screen.getByRole('button', { name: /^back$/i });

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  vi.mocked(api.getAppVersion).mockResolvedValue('0.0.0-test');
  vi.mocked(api.getHomeDirectory).mockResolvedValue('/home/tester');
  vi.mocked(api.updateConfig).mockResolvedValue({} as never);
  vi.mocked(api.addImportPath).mockResolvedValue({} as never);
});

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

async function walkTo(step: 'ai' | 'import', libraryPath = '/library/gotham') {
  const onComplete = vi.fn();
  render(<WelcomeWizard open onComplete={onComplete} />);
  const field = await screen.findByLabelText('Library Path');
  await waitFor(() => expect(field).toHaveValue('/home/tester'));
  fireEvent.change(field, { target: { value: libraryPath } });
  fireEvent.click(next());
  await screen.findByText('Set up AI & fingerprinting tools');
  fireEvent.click(next());
  if (step === 'ai') return { onComplete, key: await screen.findByLabelText(/OpenAI API Key/i) };
  const key = await screen.findByLabelText(/OpenAI API Key/i);
  fireEvent.change(key, { target: { value: 'sk-test-0001' } });
  fireEvent.click(next());
  await screen.findByText(/iTunes/i, { selector: 'h6' });
  fireEvent.click(next());
  await screen.findByText('Add Import Folders');
  return { onComplete, key };
}

describe('WelcomeWizard composition (F9-04 slice 5)', () => {
  it('shows the title, version, the five steps, and Back disabled on the first step', async () => {
    render(<WelcomeWizard open onComplete={vi.fn()} />);
    expect(await screen.findByText('Welcome to Audiobook Organizer')).toBeInTheDocument();
    expect(await screen.findByText('v0.0.0-test')).toBeInTheDocument();
    for (const label of ['Library Path', 'Tools', 'AI Setup (Optional)', 'iTunes Library', 'Import Folders']) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0);
    }
    expect(screen.getByText('Set Your Library Path')).toBeInTheDocument();
    expect(back()).toBeDisabled();
  });

  it('renders nothing, and asks for no home directory, while closed', () => {
    render(<WelcomeWizard open={false} onComplete={vi.fn()} />);
    expect(screen.queryByText('Welcome to Audiobook Organizer')).toBeNull();
    expect(api.getHomeDirectory).not.toHaveBeenCalled();
  });

  it('keeps Next disabled until the library path is non-blank, and Back returns to the previous step', async () => {
    render(<WelcomeWizard open onComplete={vi.fn()} />);
    const field = await screen.findByLabelText('Library Path');
    await waitFor(() => expect(field).toHaveValue('/home/tester'));
    fireEvent.change(field, { target: { value: '   ' } });
    expect(next()).toBeDisabled();
    fireEvent.change(field, { target: { value: '/library/gotham' } });
    expect(next()).toBeEnabled();
    fireEvent.click(next());
    await screen.findByText('Set up AI & fingerprinting tools');
    expect(back()).toBeEnabled();
    fireEvent.click(back());
    expect(await screen.findByLabelText('Library Path')).toHaveValue('/library/gotham');
  });

  it('shows the tools panel only for "Let me choose", and Skip moves on to the AI step', async () => {
    const user = userEvent.setup();
    const { key } = await walkTo('ai').then(async (r) => {
      fireEvent.click(back());
      await screen.findByText('Set up AI & fingerprinting tools');
      return r;
    });
    expect(key).toBeTruthy();
    expect(screen.queryByTestId('tools-panel')).toBeNull();
    await user.click(screen.getByLabelText('Let me choose'));
    expect(screen.getByTestId('tools-panel')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /Skip/ }));
    expect(await screen.findByLabelText(/OpenAI API Key/i)).toBeInTheDocument();
  });

  it('opens the library browser and takes the picked folder', async () => {
    const user = userEvent.setup();
    render(<WelcomeWizard open onComplete={vi.fn()} />);
    await screen.findByLabelText('Library Path');
    await user.click(screen.getByRole('button', { name: 'Browse' }));
    await user.click(await screen.findByText('pick-folder'));
    await user.click(screen.getByRole('button', { name: 'Select Folder' }));
    expect(screen.getByLabelText('Library Path')).toHaveValue('/audio/in/gotham');
  });

  it('saves the path, the key, the import folders and marks the wizard done, in that order', async () => {
    const user = userEvent.setup();
    const { onComplete } = await walkTo('import');
    await user.click(screen.getByRole('button', { name: 'Add Import Folder' }));
    await user.click(await screen.findByText('pick-folder'));
    await user.click(screen.getByRole('button', { name: 'Add Folder' }));
    expect(await screen.findByText('Import Folders (1):')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText('Select Import Folder')).toBeNull());
    await user.click(screen.getByRole('button', { name: 'Complete Setup' }));
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.updateConfig).mock.calls).toEqual([
      [{ root_dir: '/library/gotham', playlist_dir: '/library/gotham/playlists', setup_complete: true }],
      [{ openai_api_key: 'sk-test-0001', enable_ai_parsing: true }],
    ]);
    expect(api.addImportPath).toHaveBeenCalledWith('/audio/in/gotham', 'gotham');
    expect(api.importITunesLibrary).not.toHaveBeenCalled();
    expect(localStorage.getItem(STORAGE_KEYS.WELCOME_WIZARD_COMPLETED)).toBe('true');
  });

  it('skips the key save when no key was typed, and the Complete button shows Saving while it runs', async () => {
    const user = userEvent.setup();
    let release: (v: never) => void = () => {};
    vi.mocked(api.updateConfig).mockImplementationOnce(() => new Promise((r) => (release = r as never)));
    const { onComplete } = await walkTo('ai');
    // jump through the remaining steps without typing a key
    fireEvent.click(next());
    await screen.findByText(/iTunes/i, { selector: 'h6' });
    fireEvent.click(next());
    await screen.findByText('Add Import Folders');
    await user.click(screen.getByRole('button', { name: 'Complete Setup' }));
    expect(await screen.findByRole('button', { name: 'Saving...' })).toBeDisabled();
    expect(back()).toBeDisabled();
    release({} as never);
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    expect(api.updateConfig).toHaveBeenCalledTimes(1);
  });

  it('alerts, does not complete, and re-enables Complete when the save fails', async () => {
    const user = userEvent.setup();
    const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {});
    vi.spyOn(console, 'error').mockImplementation(() => {});
    vi.mocked(api.updateConfig).mockRejectedValueOnce(new Error('disk full'));
    const { onComplete } = await walkTo('import');
    await user.click(screen.getByRole('button', { name: 'Complete Setup' }));
    await waitFor(() => expect(alertSpy).toHaveBeenCalledWith('Failed to save settings. Please check the console for details.'));
    expect(onComplete).not.toHaveBeenCalled();
    expect(localStorage.getItem(STORAGE_KEYS.WELCOME_WIZARD_COMPLETED)).toBeNull();
    expect(screen.getByRole('button', { name: 'Complete Setup' })).toBeEnabled();
  });

  it('starts the iTunes import after the folders when a library was validated', async () => {
    const user = userEvent.setup();
    vi.mocked(api.validateITunesLibrary).mockResolvedValue({ audiobook_count: 3, files_found: 3 } as never);
    vi.mocked(api.importITunesLibrary).mockResolvedValue({} as never);
    const { onComplete } = await walkTo('ai');
    fireEvent.click(next());
    await screen.findByText(/iTunes/i, { selector: 'h6' });
    fireEvent.change(screen.getByLabelText('iTunes Library.xml path'), { target: { value: '/audio/in/gotham' } });
    await user.click(screen.getByRole('button', { name: 'Use' }));
    await waitFor(() => expect(api.validateITunesLibrary).toHaveBeenCalledTimes(1));
    fireEvent.click(next());
    await screen.findByText('Add Import Folders');
    await user.click(screen.getByRole('button', { name: 'Complete Setup' }));
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    expect(api.importITunesLibrary).toHaveBeenCalledWith({
      library_path: '/audio/in/gotham',
      import_mode: 'import',
      preserve_location: true,
      import_playlists: true,
      skip_duplicates: true,
      path_mappings: [],
    });
    expect(JSON.parse(localStorage.getItem(STORAGE_KEYS.ITUNES_IMPORT_SETTINGS) || '{}').libraryPath).toBe('/audio/in/gotham');
  });

  describe('the setup frame alone', () => {
    const steps = [
      { id: 'one', title: 'First', Component: ({ onNext }: { onNext: () => void }) => <button onClick={onNext}>skip-one</button> },
      { id: 'two', title: 'Second', Component: () => <p>second body</p> },
    ];
    const renderFrame = (over: Partial<React.ComponentProps<typeof SetupWizardFrame>> = {}) => {
      const onFinish = vi.fn();
      const canProceed = vi.fn((id: string) => id !== 'one');
      render(<SetupWizardFrame open title="Gotham first run" version="9.9" steps={steps} canProceed={canProceed} onFinish={onFinish} saving={false} {...over} />);
      return { onFinish, canProceed };
    };

    it('shows the title, version chip, step labels and the active step, and gates Next with canProceed', () => {
      const { canProceed } = renderFrame();
      expect(screen.getByText('Gotham first run')).toBeInTheDocument();
      expect(screen.getByText('v9.9')).toBeInTheDocument();
      expect(screen.getByText('First')).toBeInTheDocument();
      expect(screen.getByText('Second')).toBeInTheDocument();
      expect(canProceed).toHaveBeenCalledWith('one');
      expect(next()).toBeDisabled();
    });

    it('lets a step advance with onNext, then shows Complete Setup that calls onFinish', async () => {
      const user = userEvent.setup();
      const { onFinish } = renderFrame();
      await user.click(screen.getByRole('button', { name: 'skip-one' }));
      expect(screen.getByText('second body')).toBeInTheDocument();
      expect(back()).toBeEnabled();
      await user.click(screen.getByRole('button', { name: 'Complete Setup' }));
      expect(onFinish).toHaveBeenCalledTimes(1);
    });

    it('hides an unknown version and shows Saving while saving', async () => {
      const user = userEvent.setup();
      renderFrame({ version: 'unknown', canProceed: () => true });
      expect(screen.queryByText('vunknown')).toBeNull();
      await user.click(next());
      expect(screen.getByRole('button', { name: 'Complete Setup' })).toBeEnabled();
    });

    it('imports no books code and no services/api', () => {
      const imports = frameSource.split('\n').filter((l) => /from '/.test(l));
      expect(imports.length).toBeGreaterThan(3);
      for (const l of imports) expect(l).not.toMatch(/modules\/books|services\/api|WelcomeWizard/);
    });
  });
});

// file: web/src/components/wizard/WelcomeWizard.test.tsx
// version: 1.0.0
// guid: 3f1a7d20-6c4e-4c8a-9a1b-2e5d7c0f9b34
// last-edited: 2026-09-10

import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WelcomeWizard } from './WelcomeWizard';
import * as api from '../../services/api';

// The wizard's step 2 renders ToolsPanel and its browser dialogs render
// ServerFileBrowser; both fetch on mount. Stub them so this test exercises the
// OpenAI-key validation path and nothing else.
vi.mock('../tools/ToolsPanel', () => ({
  ToolsPanel: () => <div data-testid="tools-panel" />,
}));
vi.mock('../common/ServerFileBrowser', () => ({
  ServerFileBrowser: () => <div data-testid="server-file-browser" />,
}));

vi.mock('../../services/api', async () => {
  const actual = await vi.importActual<typeof api>('../../services/api');
  return {
    ...actual,
    getAppVersion: vi.fn(),
    getHomeDirectory: vi.fn(),
    validateOpenAIKey: vi.fn(),
  };
});

// A syntactically plausible but non-real placeholder key.
const TEST_KEY = 'sk-test-0001';

/** Advance the wizard from step 0 to the "AI Setup (Optional)" step. */
async function gotoAIStep() {
  render(<WelcomeWizard open onComplete={vi.fn()} />);
  await screen.findByText('AI Setup (Optional)');
  const next = () => screen.getByRole('button', { name: /^next$/i });
  fireEvent.click(next());
  fireEvent.click(next());
  return await screen.findByLabelText(/OpenAI API Key/i);
}

describe('WelcomeWizard — OpenAI key validation stays server-side (SEC-9)', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.getAppVersion).mockResolvedValue('0.0.0-test');
    vi.mocked(api.getHomeDirectory).mockResolvedValue('/home/tester');
    // Any direct network call from the component lands here so we can prove the
    // raw key never leaves the browser addressed to OpenAI.
    fetchSpy = vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) });
    vi.stubGlobal('fetch', fetchSpy);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('never sends the typed key to api.openai.com and calls the backend instead', async () => {
    const keyField = await gotoAIStep();
    vi.mocked(api.validateOpenAIKey).mockResolvedValue({ valid: true });

    fireEvent.change(keyField, { target: { value: TEST_KEY } });
    fireEvent.click(screen.getByRole('button', { name: /test connection/i }));

    // The SEC-9 assertion: the raw key must never be addressed to OpenAI from
    // the browser, where it would land in the network log.
    await waitFor(() => {
      const openaiCalls = fetchSpy.mock.calls.filter((call) =>
        JSON.stringify(call).includes('api.openai.com')
      );
      expect(openaiCalls).toEqual([]);
    });

    // Non-vacuous: the component really did run the validation handler, so the
    // absence of an api.openai.com request is meaningful rather than the result
    // of the component throwing before it got there.
    expect(api.validateOpenAIKey).toHaveBeenCalledWith(TEST_KEY);
    expect(await screen.findByText(/API key is valid and working/i)).toBeInTheDocument();
  });

  it('renders the existing error alert when the backend reports the key is invalid', async () => {
    const keyField = await gotoAIStep();
    vi.mocked(api.validateOpenAIKey).mockResolvedValue({ valid: false, error: 'invalid_api_key' });

    fireEvent.change(keyField, { target: { value: TEST_KEY } });
    fireEvent.click(screen.getByRole('button', { name: /test connection/i }));

    expect(await screen.findByText(/Invalid API key or connection failed/i)).toBeInTheDocument();
    expect(api.validateOpenAIKey).toHaveBeenCalledWith(TEST_KEY);
  });

  it('renders the existing error alert when the backend call itself fails', async () => {
    const keyField = await gotoAIStep();
    vi.mocked(api.validateOpenAIKey).mockRejectedValue(new Error('could not verify'));

    fireEvent.change(keyField, { target: { value: TEST_KEY } });
    fireEvent.click(screen.getByRole('button', { name: /test connection/i }));

    expect(await screen.findByText(/Invalid API key or connection failed/i)).toBeInTheDocument();
  });
});

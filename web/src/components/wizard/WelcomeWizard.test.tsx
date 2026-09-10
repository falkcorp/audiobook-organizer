// file: web/src/components/wizard/WelcomeWizard.test.tsx
// version: 1.1.0
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

    // Wait for the POSITIVE first. An "expect nothing happened" inside waitFor
    // passes on its first tick, before the component could have called
    // anything, so the negative assertions below are only meaningful once the
    // validation handler has demonstrably run.
    await waitFor(() => {
      expect(api.validateOpenAIKey).toHaveBeenCalledWith(TEST_KEY);
    });

    // The SEC-9 assertion: the raw key must never leave the browser on a direct
    // network call — to OpenAI or to anywhere else. Filtering on the key rather
    // than on the string 'api.openai.com' is strictly broader (a proxy, a
    // subdomain or a redirect target would still be caught) and it drops the
    // hostname literal that CodeQL reads as an incomplete URL check
    // (js/incomplete-url-substring-sanitization).
    const leakedCalls = fetchSpy.mock.calls.filter((call) =>
      JSON.stringify(call).includes(TEST_KEY)
    );
    expect(leakedCalls).toEqual([]);

    // Stronger still, and the reason the filter above can be trusted: this path
    // issues no direct fetch at all, so there is no request for the key to ride
    // on. Measured, not assumed — see the commit that introduced this line.
    expect(fetchSpy).not.toHaveBeenCalled();
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

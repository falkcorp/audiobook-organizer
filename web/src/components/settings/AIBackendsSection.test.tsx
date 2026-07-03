// file: web/src/components/settings/AIBackendsSection.test.tsx
// version: 1.0.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-07-03

import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AIBackendsSection } from './AIBackendsSection';
import type { AIBackendConfig, AIBackendsStatus } from '../../services/api';
import * as api from '../../services/api';

vi.mock('../../services/api', async () => {
  const actual = await vi.importActual<typeof api>('../../services/api');
  return {
    ...actual,
    getAIBackendsStatus: vi.fn(),
    pullAIBackendModel: vi.fn(),
  };
});

const defaultConfig: AIBackendConfig = {
  embedding_mode: 'disabled',
  llm_mode: 'disabled',
  local_base_url: '',
  local_embedding_model: '',
  local_llm_model: '',
};

describe('AIBackendsSection', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders the section heading', () => {
    render(<AIBackendsSection config={defaultConfig} onChange={vi.fn()} />);
    expect(screen.getByText('AI Backends')).toBeInTheDocument();
  });

  it('renders both mode selects', () => {
    render(<AIBackendsSection config={defaultConfig} onChange={vi.fn()} />);
    expect(screen.getByLabelText('Embedding mode')).toBeInTheDocument();
    expect(screen.getByLabelText('LLM mode')).toBeInTheDocument();
  });

  it('calls onChange with the new embedding mode when selected', async () => {
    const onChange = vi.fn();
    render(<AIBackendsSection config={defaultConfig} onChange={onChange} />);
    const select = screen.getByLabelText('Embedding mode');
    fireEvent.mouseDown(select);
    const option = await screen.findByRole('option', { name: 'Local (Ollama)' });
    fireEvent.click(option);
    expect(onChange).toHaveBeenCalledWith({ embedding_mode: 'local' });
  });

  it('hides local fields when neither mode is local-involving', () => {
    render(<AIBackendsSection config={defaultConfig} onChange={vi.fn()} />);
    expect(screen.queryByLabelText('Local base URL')).not.toBeInTheDocument();
  });

  it('shows local fields when embedding mode is local', () => {
    render(
      <AIBackendsSection
        config={{ ...defaultConfig, embedding_mode: 'local' }}
        onChange={vi.fn()}
      />
    );
    expect(screen.getByLabelText('Local base URL')).toBeInTheDocument();
    expect(screen.getByLabelText('Local embedding model')).toBeInTheDocument();
    expect(screen.getByLabelText('Local LLM model')).toBeInTheDocument();
  });

  it('shows local fields when llm mode is openai-fallback-local', () => {
    render(
      <AIBackendsSection
        config={{ ...defaultConfig, llm_mode: 'openai-fallback-local' }}
        onChange={vi.fn()}
      />
    );
    expect(screen.getByLabelText('Local base URL')).toBeInTheDocument();
  });

  it('calls getAIBackendsStatus and displays effective mode on Test Connection', async () => {
    const status: AIBackendsStatus = {
      embedding_mode: 'local',
      llm_mode: 'disabled',
      local_base_url: 'http://192.168.0.20:11434/v1',
      local_reachable: true,
    };
    vi.mocked(api.getAIBackendsStatus).mockResolvedValue(status);

    render(
      <AIBackendsSection config={{ ...defaultConfig, embedding_mode: 'local' }} onChange={vi.fn()} />
    );
    fireEvent.click(screen.getByText('Test Connection'));

    await waitFor(() => {
      expect(screen.getByText('Embedding: local')).toBeInTheDocument();
    });
    expect(screen.getByText('Local endpoint reachable')).toBeInTheDocument();
  });

  it('shows a pull-model dialog when status reports the model absent, and pulls on confirm', async () => {
    const status: AIBackendsStatus = {
      embedding_mode: 'local',
      llm_mode: 'disabled',
      local_base_url: 'http://192.168.0.20:11434/v1',
      local_reachable: true,
      embedding_model: { name: 'bge-m3', pulled: false },
    };
    vi.mocked(api.getAIBackendsStatus).mockResolvedValue(status);
    vi.mocked(api.pullAIBackendModel).mockResolvedValue({ model: 'bge-m3', pulled: true });

    render(
      <AIBackendsSection config={{ ...defaultConfig, embedding_mode: 'local' }} onChange={vi.fn()} />
    );
    fireEvent.click(screen.getByText('Test Connection'));

    await waitFor(() => {
      expect(screen.getByText('Not pulled')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByText('Pull now'));
    expect(screen.getByText('bge-m3 not pulled — Pull now?')).toBeInTheDocument();

    fireEvent.click(screen.getByText('Confirm'));

    await waitFor(() => {
      expect(api.pullAIBackendModel).toHaveBeenCalledWith('bge-m3');
    });
  });
});

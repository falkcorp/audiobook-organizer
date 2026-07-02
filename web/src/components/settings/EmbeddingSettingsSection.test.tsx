// file: web/src/components/settings/EmbeddingSettingsSection.test.tsx
// version: 1.1.0
// guid: a9b8c7d6-e5f4-3210-abcd-ef9876543210
// last-edited: 2026-07-01

import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { EmbeddingSettingsSection } from './EmbeddingSettingsSection';
import type { EmbeddingConfig } from '../../services/api';

const defaultConfig: EmbeddingConfig = {
  enabled: false,
  model: 'bge-m3',
  dimensions: 1024,
  base_url: '',
  vector_backend: 'hnsw',
};

describe('EmbeddingSettingsSection', () => {
  it('renders the section heading', () => {
    const onChange = vi.fn();
    render(<EmbeddingSettingsSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByText('Embedding Settings')).toBeInTheDocument();
  });

  it('renders the enable embedding generation label', () => {
    const onChange = vi.fn();
    render(<EmbeddingSettingsSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByText('Enable embedding generation')).toBeInTheDocument();
  });

  it('renders the model text field', () => {
    const onChange = vi.fn();
    render(<EmbeddingSettingsSection config={defaultConfig} onChange={onChange} />);
    expect(screen.getByLabelText('Model')).toBeInTheDocument();
  });

  it('calls onChange with enabled: true when Switch is toggled on', () => {
    const onChange = vi.fn();
    render(<EmbeddingSettingsSection config={defaultConfig} onChange={onChange} />);
    const switchInput = screen.getByRole('checkbox', { name: /enable embedding generation/i });
    fireEvent.click(switchInput);
    expect(onChange).toHaveBeenCalledWith({ enabled: true });
  });

  it('calls onChange with enabled: false when Switch is toggled off', () => {
    const onChange = vi.fn();
    render(
      <EmbeddingSettingsSection config={{ ...defaultConfig, enabled: true }} onChange={onChange} />
    );
    const switchInput = screen.getByRole('checkbox', { name: /enable embedding generation/i });
    fireEvent.click(switchInput);
    expect(onChange).toHaveBeenCalledWith({ enabled: false });
  });

  it('calls onChange with numeric dimensions when field changes', () => {
    const onChange = vi.fn();
    render(<EmbeddingSettingsSection config={defaultConfig} onChange={onChange} />);
    const dimField = screen.getByLabelText('Dimensions');
    fireEvent.change(dimField, { target: { value: '512' } });
    expect(onChange).toHaveBeenCalledWith({ dimensions: 512 });
    // Ensure it's a number, not a string
    const callArg = onChange.mock.calls[0][0] as { dimensions: unknown };
    expect(typeof callArg.dimensions).toBe('number');
  });

  it('calls onChange with updated model string when model field changes', () => {
    const onChange = vi.fn();
    render(<EmbeddingSettingsSection config={defaultConfig} onChange={onChange} />);
    const modelField = screen.getByLabelText('Model');
    fireEvent.change(modelField, { target: { value: 'text-embedding-3-large' } });
    expect(onChange).toHaveBeenCalledWith({ model: 'text-embedding-3-large' });
  });

  it('renders a link to download the latest Ollama', () => {
    const onChange = vi.fn();
    render(<EmbeddingSettingsSection config={defaultConfig} onChange={onChange} />);
    const link = screen.getByRole('link', { name: /download the latest ollama/i });
    expect(link).toHaveAttribute('href', 'https://ollama.com/download');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
  });
});

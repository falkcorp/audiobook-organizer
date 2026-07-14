// file: web/src/components/ReviewBanner.test.tsx
// version: 1.0.0
// guid: 3a7e2c81-9d46-4b50-8f13-6c0a5d9e2b47
// last-edited: 2026-07-13

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { ReviewBanner } from './ReviewBanner';
import { useReviewStore } from '../stores/useReviewStore';

// Spy on useNavigate so the click-navigates assertion is a direct call check
// rather than route introspection.
const navigateSpy = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => navigateSpy };
});

describe('ReviewBanner', () => {
  beforeEach(() => {
    navigateSpy.mockReset();
    useReviewStore.setState({ count: 0, byKind: {}, items: [], itemsLoading: false, _pollTimer: null });
  });

  it('renders nothing when the count is 0', () => {
    useReviewStore.setState({ count: 0 });
    const { container } = renderWithProviders(<ReviewBanner />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the aggregate count when there are pending items', () => {
    useReviewStore.setState({ count: 5 });
    renderWithProviders(<ReviewBanner />);
    expect(screen.getByText(/You have 5 items to review/)).toBeInTheDocument();
  });

  it('uses the singular form for a single item', () => {
    useReviewStore.setState({ count: 1 });
    renderWithProviders(<ReviewBanner />);
    expect(screen.getByText(/You have 1 item to review/)).toBeInTheDocument();
  });

  it('navigates to /review when clicked', () => {
    useReviewStore.setState({ count: 3 });
    renderWithProviders(<ReviewBanner />);
    fireEvent.click(screen.getByText(/You have 3 items to review/));
    expect(navigateSpy).toHaveBeenCalledWith('/review');
  });
});

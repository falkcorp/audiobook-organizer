// file: web/src/components/__tests__/CoverLightbox.test.tsx
// version: 1.0.1
// guid: 8a9b0c1d-2e3f-4a5b-6c7d-8e9f0a1b2c3d

import { render, screen, fireEvent } from '@testing-library/react';
import { vi } from 'vitest';
import { CoverLightbox } from '../CoverLightbox';

describe('CoverLightbox', () => {
  it('should render nothing when open is false', () => {
    const { container } = render(<CoverLightbox open={false} src="image.jpg" onClose={() => {}} />);
    expect(container.querySelector('[role="presentation"]')).not.toBeInTheDocument();
  });

  it('should render image when open is true', () => {
    render(<CoverLightbox open={true} src="https://example.com/cover.jpg" onClose={() => {}} />);
    expect(screen.getByRole('img')).toHaveAttribute('src', 'https://example.com/cover.jpg');
  });

  it('should call onClose when close button clicked', () => {
    const onClose = vi.fn();
    render(<CoverLightbox open={true} src="image.jpg" onClose={onClose} />);
    const closeBtn = screen.getByLabelText('Close');
    fireEvent.click(closeBtn);
    expect(onClose).toHaveBeenCalled();
  });

  it('should have close button with proper aria label', () => {
    // Modal handles backdrop clicks automatically and calls onClose
    // This test verifies the close button is properly configured
    const onClose = vi.fn();
    render(<CoverLightbox open={true} src="image.jpg" onClose={onClose} />);
    const closeBtn = screen.getByLabelText('Close');
    expect(closeBtn).toBeInTheDocument();
  });

  it('should render placeholder when src is null', () => {
    render(<CoverLightbox open={true} src={null} onClose={() => {}} />);
    expect(screen.getByTestId('cover-placeholder')).toBeInTheDocument();
  });
});

// file: web/src/components/__tests__/CoverLightbox.test.tsx
// version: 1.1.0
// guid: 8a9b0c1d-2e3f-4a5b-6c7d-8e9f0a1b2c3d
// last-edited: 2026-09-13

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

  it('bounds the image by the viewport rather than by its container', () => {
    render(<CoverLightbox open={true} src="https://example.com/cover.jpg" onClose={() => {}} />);
    const img = screen.getByTestId('cover-lightbox-img') as HTMLImageElement;
    expect(img.style.maxWidth).toBe('90vw');
    expect(img.style.maxHeight).toBe('90vh');
    expect(img.style.objectFit).toBe('contain');
  });

  it('says the cover failed to load instead of collapsing to a broken-image box', () => {
    render(<CoverLightbox open={true} src="https://example.com/dead.jpg" onClose={() => {}} />);
    fireEvent.error(screen.getByTestId('cover-lightbox-img'));
    expect(screen.queryByTestId('cover-lightbox-img')).not.toBeInTheDocument();
    expect(screen.getByTestId('cover-placeholder')).toHaveTextContent(
      'The cover image failed to load: https://example.com/dead.jpg'
    );
  });

  it('closes on Escape', () => {
    const onClose = vi.fn();
    render(<CoverLightbox open={true} src="image.jpg" onClose={onClose} />);
    fireEvent.keyDown(screen.getByTestId('cover-lightbox'), { key: 'Escape' });
    expect(onClose).toHaveBeenCalled();
  });
});

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import AccessStrip, { formatRemaining } from './AccessStrip';

describe('AccessStrip', () => {
  it('renders the empty state when no previewUrl is provided', () => {
    render(<AccessStrip />);
    expect(screen.getByLabelText('access-strip-empty')).toBeInTheDocument();
    expect(screen.queryByLabelText('access-strip')).not.toBeInTheDocument();
  });

  it('renders URL + Copy URL when previewUrl is provided', () => {
    const onCopyUrl = vi.fn();
    render(
      <AccessStrip
        previewUrl="https://previews.agency.techar.ch/sites/abcd-1234"
        onCopyUrl={onCopyUrl}
      />
    );
    expect(
      screen.getByText('https://previews.agency.techar.ch/sites/abcd-1234')
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Copy URL' }));
    expect(onCopyUrl).toHaveBeenCalledWith('https://previews.agency.techar.ch/sites/abcd-1234');
  });

  it('masks the passcode until Show is clicked', () => {
    render(
      <AccessStrip
        previewUrl="https://previews.example.com/sites/x"
        passcode="H7Q32KX9"
        cleartextRevealableUntil={new Date(Date.now() + 24 * 60 * 60 * 1000)}
      />
    );
    const code = screen.getByLabelText('passcode');
    expect(code.textContent).toBe('••••-••••');
    fireEvent.click(screen.getByRole('button', { name: 'Show' }));
    expect(code.textContent).toBe('H7Q32KX9');
    fireEvent.click(screen.getByRole('button', { name: 'Hide' }));
    expect(code.textContent).toBe('••••-••••');
  });

  it('fires onCopyCode with the passcode', () => {
    const onCopyCode = vi.fn();
    render(
      <AccessStrip
        previewUrl="https://previews.example.com/sites/x"
        passcode="H7Q32KX9"
        cleartextRevealableUntil={new Date(Date.now() + 24 * 60 * 60 * 1000)}
        onCopyCode={onCopyCode}
      />
    );
    fireEvent.click(screen.getByRole('button', { name: 'Copy code' }));
    expect(onCopyCode).toHaveBeenCalledWith('H7Q32KX9');
  });

  it('shows "wiped" copy when cleartextRevealableUntil is in the past', () => {
    render(
      <AccessStrip
        previewUrl="https://previews.example.com/sites/x"
        passcode="H7Q32KX9"
        cleartextRevealableUntil={new Date(Date.now() - 60 * 1000)}
      />
    );
    expect(screen.getByLabelText('passcode').textContent).toContain('Code wiped');
    // Copy / show / hide controls are hidden once the cleartext is wiped.
    expect(screen.queryByRole('button', { name: 'Copy code' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Show' })).not.toBeInTheDocument();
    // Regenerate-code button stays so the operator can refresh.
    expect(screen.getByRole('button', { name: 'Regenerate code' })).toBeInTheDocument();
  });

  it('fires onRegenerateCode', () => {
    const onRegenerateCode = vi.fn();
    render(
      <AccessStrip
        previewUrl="https://previews.example.com/sites/x"
        onRegenerateCode={onRegenerateCode}
      />
    );
    fireEvent.click(screen.getByRole('button', { name: 'Regenerate code' }));
    expect(onRegenerateCode).toHaveBeenCalled();
  });
});

describe('formatRemaining', () => {
  // Frozen clock so the elapsed time between Date.now() at target
  // construction and Date.now() inside the function under test is
  // exactly zero — otherwise the result rounds down by an hour.
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-01-01T00:00:00Z'));
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('renders future-day-and-hour breakdowns', () => {
    const target = new Date(Date.now() + (4 * 24 + 12) * 60 * 60 * 1000);
    expect(formatRemaining(target)).toBe('4d 12h');
  });

  it('returns 0d 0h for past targets', () => {
    expect(formatRemaining(new Date(Date.now() - 60_000))).toBe('0d 0h');
  });
});

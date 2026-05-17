import { describe, it, expect } from 'vitest';
import { cookieScopeDomain } from './auth';

// These cases are the production + preview topologies. If the auth_token
// cookie isn't scoped to the shared parent the browser never sends it
// to the BFF host and every authenticated route 401s — the exact prod
// defect this guards against.
describe('cookieScopeDomain', () => {
  it('scopes prod SPA→BFF to the shared parent', () => {
    expect(cookieScopeDomain('agency.techar.ch', 'bff.agency.techar.ch')).toBe('agency.techar.ch');
  });

  it('scopes preview SPA→per-env BFF to the shared parent', () => {
    expect(cookieScopeDomain('preview.agency.techar.ch', 'feat-x.bff.agency.techar.ch')).toBe(
      'agency.techar.ch'
    );
  });

  it('is host-only for localhost dev (no usable shared parent)', () => {
    expect(cookieScopeDomain('localhost', 'localhost')).toBe('');
    expect(cookieScopeDomain('localhost', 'test-bff.example.com')).toBe('');
  });

  it('refuses a bare public suffix as a Domain', () => {
    // Only `ch` in common → not a valid cookie Domain.
    expect(cookieScopeDomain('a.ch', 'b.ch')).toBe('');
  });

  it('refuses a bare two-level public suffix (.co.uk) and accepts the registrable parent', () => {
    // Shared tail is only `co.uk` → not a valid Domain.
    expect(cookieScopeDomain('a.co.uk', 'b.co.uk')).toBe('');
    // Shared tail includes the registrable label → valid.
    expect(cookieScopeDomain('agency.example.co.uk', 'bff.agency.example.co.uk')).toBe(
      'agency.example.co.uk'
    );
  });

  it('is empty when either host is missing', () => {
    expect(cookieScopeDomain('', 'bff.agency.techar.ch')).toBe('');
    expect(cookieScopeDomain('agency.techar.ch', '')).toBe('');
  });
});

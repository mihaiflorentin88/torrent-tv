import { afterEach, describe, expect, it, vi } from 'vitest';
import { detectCapabilities } from './capability';

describe('capability detection', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('reports abort controller support when the feature is present', () => {
    expect(detectCapabilities({ abortController: true })).toEqual({ supportsAbortController: true });
  });

  it('reports no abort controller support when the feature is absent', () => {
    expect(detectCapabilities({ abortController: false })).toEqual({ supportsAbortController: false });
  });

  it('derives flags from runtime globals when no presence map is injected', () => {
    expect(detectCapabilities()).toEqual({ supportsAbortController: typeof AbortController === 'function' });
  });

  it('derives flags from runtime globals for an empty presence map', () => {
    expect(detectCapabilities({})).toEqual({ supportsAbortController: typeof AbortController === 'function' });
  });

  it('detects absence when the runtime global is missing', () => {
    vi.stubGlobal('AbortController', undefined);
    expect(detectCapabilities()).toEqual({ supportsAbortController: false });
  });
});

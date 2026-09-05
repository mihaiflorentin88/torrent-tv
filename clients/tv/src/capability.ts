export interface FeaturePresence {
  abortController?: boolean;
}

export interface Capabilities {
  supportsAbortController: boolean;
}

export function detectCapabilities(features?: FeaturePresence): Capabilities {
  const runtimeSupportsAbortController = typeof AbortController === 'function';
  return { supportsAbortController: features?.abortController ?? runtimeSupportsAbortController };
}

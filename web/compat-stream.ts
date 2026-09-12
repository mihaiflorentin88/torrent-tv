// Transport for the compatibility transcode stream (fragmented MP4 with
// stream-copied video and AAC audio). Chromium cannot play this stream
// through <video src>: the container declares no duration, the element
// derives a bogus few-seconds one from the first buffered fragments, stops
// fetching at fragment boundaries, and the playhead keeps catching up to the
// buffered end — playback stalls every few seconds and the recovery loop
// reloads forever. Feeding the same bytes through MediaSource with the real
// duration declared (the app knows it from media-info) buffers minutes ahead
// and plays continuously.
export type CompatStream = { stop(): void };

export function compatStreamSupported(): boolean {
  return typeof MediaSource !== 'undefined' && typeof MediaSource.isTypeSupported === 'function' && MediaSource.isTypeSupported('video/mp4; codecs="avc1.640028,mp4a.40.2"');
}

export function attachCompatStream(video: HTMLVideoElement, url: string, durationMs: number, onError: (message: string) => void): CompatStream | null {
  if (!compatStreamSupported()) return null;
  const mediaSource = new MediaSource();
  const objectURL = URL.createObjectURL(mediaSource);
  video.src = objectURL;
  const controller = new AbortController();
  let sourceBuffer: SourceBuffer | null = null;
  let queue: Uint8Array[] = [];
  let appending = false;
  let stopped = false;
  let buffer = 0;
  const pump = () => {
    if (stopped || appending || !sourceBuffer || queue.length === 0) return;
    appending = true;
    const chunk = queue.shift()!;
    try {
      sourceBuffer.appendBuffer(chunk as unknown as ArrayBuffer);
    } catch {
      // Buffer quota: drop already-watched data and retry after the removal.
      appending = false;
      try {
        if (video.currentTime > 60) sourceBuffer.remove(0, video.currentTime - 30);
      } catch { }
    }
  };
  mediaSource.addEventListener('sourceopen', () => {
    if (stopped) return;
    try {
      sourceBuffer = mediaSource.addSourceBuffer('video/mp4; codecs="avc1.640028,mp4a.40.2"');
    } catch (error) {
      onError(`This browser cannot accept the compatibility stream: ${(error as Error).message}`);
      return;
    }
    mediaSource.duration = durationMs / 1000;
    sourceBuffer.addEventListener('updateend', () => {
      appending = false;
      pump();
    });
    sourceBuffer.addEventListener('error', () => onError('The compatibility stream could not be decoded by this browser.'));
    void stream();
  });
  const stream = async () => {
    try {
      const response = await fetch(url, { signal: controller.signal });
      if (!response.ok || !response.body) {
        onError(`The compatibility stream is unavailable (HTTP ${response.status}).`);
        return;
      }
      const reader = response.body.getReader();
      while (true) {
        const { done, value } = await reader.read();
        if (stopped) return;
        if (done) {
          if (mediaSource.readyState === 'open') mediaSource.endOfStream();
          return;
        }
        if (!value || value.length === 0) continue;
        buffer += value.length;
        queue.push(value);
        pump();
      }
    } catch (error) {
      if (!stopped && (error as Error).name !== 'AbortError') onError(`The compatibility stream stopped: ${(error as Error).message}`);
    }
  };
  return {
    stop() {
      stopped = true;
      controller.abort();
      try {
        sourceBuffer?.abort();
      } catch { }
      URL.revokeObjectURL(objectURL);
      if (video.src === objectURL) video.removeAttribute('src');
    },
  };
}

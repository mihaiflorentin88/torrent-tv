interface Window {
  webapis: any;
  tizen: any;
  FileListBoot?: {
    stage(value: string): void;
    ready(): void;
    fail(error: unknown): void;
  };
  // Injected by the Android TV shell's platform-bridge.js (no Tizen runtime
  // there). FileListTVIdentity is declared where it is consumed — app-name.ts.
  FileListTVNative?: {
    exit(): void;
    getIp(): string;
    getSubnetMask(): string;
    open(url: string): void;
    openExternal(url: string): boolean;
    log(message: string): void;
    setDisplayRect(x: number, y: number, width: number, height: number): void;
    setDisplayMethod(mode: string): void;
    prepareAsync(successToken: string, errorToken: string): void;
    play(): void;
    pause(): void;
    seekTo(milliseconds: number): void;
    stop(): void;
    close(): void;
    getDuration(): number;
    getTotalTrackInfo(): string;
    setSelectTrack(type: string, index: number): void;
    setSilentSubtitle(silent: boolean): void;
  };
  FileListTVBridge?: { dispatch(payload: string): void };
}

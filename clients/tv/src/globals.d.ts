interface Window {
  webapis: any;
  tizen: any;
  FileListBoot?: {
    stage(value: string): void;
    ready(): void;
    fail(error: unknown): void;
  };
}

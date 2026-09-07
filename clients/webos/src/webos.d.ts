/**
 * Consumed public LG webOSTV SDK and webOS platform interfaces.
 *
 * Virtual keyboard events:
 * webOS TV emits the 'keyboardStateChange' event on `document` when the
 * on-screen keyboard shows or hides.
 * Its event payload is `{ detail: { visibility: boolean } }`.
 * Handlers should forward only boolean `detail.visibility`.
 */

export interface WebOSNetworkInterfaceStatus {
 state?: string;
 ipAddress?: string;
 netmask?: string;
}

export interface WebOSConnectionStatusResponse {
 isInternetConnectionAvailable?: boolean;
 wired?: WebOSNetworkInterfaceStatus;
 wifi?: WebOSNetworkInterfaceStatus;
 returnValue?: boolean;
 errorCode?: string | number;
 errorText?: string;
}

export interface WebOSConnectionStatusParameters {
 subscribe?: boolean;
 onSuccess?: (response: WebOSConnectionStatusResponse) => void;
 onFailure?: (error: { errorCode?: string | number; errorText?: string }) => void;
}

export interface WebOSLaunchParameters {
 id: string;
 params?: Record<string, unknown>;
 onSuccess?: (response: { returnValue?: boolean; id?: string }) => void;
 onFailure?: (error: { errorCode?: string | number; errorText?: string }) => void;
}

export interface WebOSDev {
 connection: {
  getStatus(parameters: WebOSConnectionStatusParameters): void;
 };
 launch(parameters: WebOSLaunchParameters): void;
}

declare global {
 interface Window {
  webOSDev?: WebOSDev;
 }
}

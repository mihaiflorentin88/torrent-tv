// webOS 4.x TVs run Chromium 53, which lacks Object.entries and
// Object.getOwnPropertyDescriptors (added in Chromium 54, required by
// esbuild's object spread helper) and String.prototype.padStart/padEnd
// (added in Chromium 57) used by the shared TV sources. The core-js modules
// feature-detect each capability internally and install a standards-compliant
// patch only when the native method is missing, so newer engines keep native
// behavior. This module is imported before any shared module evaluates (see
// entry-webos.ts).
import 'core-js/modules/es.object.entries';
import 'core-js/modules/es.object.get-own-property-descriptors';
import 'core-js/modules/es.string.pad-start';
import 'core-js/modules/es.string.pad-end';

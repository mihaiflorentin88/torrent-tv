// webOS 4.x TVs run Chromium 53, which lacks Object.entries (added in
// Chromium 54) and String.prototype.padStart/padEnd (added in Chromium 57)
// used by the shared TV sources. The core-js modules feature-detect each
// capability internally and install a standards-compliant patch only when the
// native method is missing, so newer engines keep native behavior. This module
// is imported before any shared module evaluates (see entry-webos.ts).
// Note: Object.getOwnPropertyDescriptors cannot be polyfilled here because
// esbuild hoists its object-spread helper (var Yc = Object.getOwnPropertyDescriptors;)
// to the top of the bundle before module code can execute; that polyfill is
// installed early in startup.js instead.
import 'core-js/modules/es.object.entries';
import 'core-js/modules/es.string.pad-start';
import 'core-js/modules/es.string.pad-end';

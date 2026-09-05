/* FileList TV fatal error panel (ticket #80).
 * Shipped verbatim (see vite.config.ts) and never transpiled: classic ES5 only.
 * Owned by the boot layer so the panel paints even when the app bundle itself
 * crashed: the first unhandled error renders a full-screen explanation and
 * reports once, best effort, to the server's client-diagnostics channel. */
(function() {
 'use strict';

 var DEFAULT_ENDPOINT = '/api/v1/diagnostics/client';
 var MAX_MESSAGE_BYTES = 1000; // server cap: 1..1000 characters per message
 var MAX_SOURCE_BYTES = 500; // keep the whole body far under the 16 KiB cap
 var SERVER_URL_KEY = 'filelist.serverUrl'; // stored by the app once connected

 var exitApp = null;
 var endpoint = DEFAULT_ENDPOINT;
 var wired = false;
 var fired = false;

 // Re-evaluating this script (fresh page load) supersedes any earlier wiring
 // so exactly one panel generation is ever live. Within a session nothing
 // removes the listeners: they outlive the app bundle on purpose.
 var previous = window.FileListFatalError;
 if (previous && typeof previous._release === 'function') previous._release();

 function utf8Truncate(text, limit) {
  var out = '';
  var bytes = 0;
  for (var i = 0; i < text.length; i++) {
   var code = text.charCodeAt(i);
   var size = code < 0x80 ? 1 : code < 0x800 ? 2 : 3;
   if (bytes + size > limit) break;
   out += text.charAt(i);
   bytes += size;
  }
  return out;
 }

 function describe(value) {
  if (!value) return 'Unknown error';
  if (typeof value === 'string') return value;
  if (value.message) return String(value.message);
  return String(value);
 }

 function reportURL() {
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(endpoint)) return endpoint;
  try {
   var server = window.localStorage ? window.localStorage.getItem(SERVER_URL_KEY) : null;
   if (server) return server.replace(/\/+$/, '') + endpoint;
  } catch (error) { /* storage unavailable: fall back to the bare path */ }
  return endpoint;
 }

 function report(message, context) {
  if (typeof fetch !== 'function') return; // engines without fetch: panel still shows
  var payload = {
   level: 'error',
   message: utf8Truncate(message, MAX_MESSAGE_BYTES),
   context: context
  };
  try {
   var result = fetch(reportURL(), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
   });
   if (result && typeof result.catch === 'function') {
    result.catch(function() { /* reporting is best effort */ });
   }
  } catch (error) { /* reporting is best effort */ }
 }

 function appendText(parent, tag, text, css) {
  var element = document.createElement(tag);
  element.textContent = text; // never innerHTML: the message must not parse
  element.style.cssText = css;
  parent.appendChild(element);
  return element;
 }

 function render(message) {
  if (!document.body) return;
  var panel = document.createElement('div');
  panel.id = 'fatal-error';
  panel.setAttribute('role', 'alert');
  // Boot-screen palette, sized for a 1080p TV (mirrors #startup in index.html).
  panel.style.cssText = 'position:fixed;top:0;right:0;bottom:0;left:0;z-index:200;'
   + 'display:flex;flex-direction:column;align-items:center;justify-content:center;'
   + 'padding:100px;box-sizing:border-box;background:#071018;color:#f5f8fa;'
   + 'font-family:Arial,sans-serif;text-align:center';
  appendText(panel, 'h1', 'Something went wrong',
   'font-size:64px;margin:0 0 26px;color:#ff9b9b');
  appendText(panel, 'p', message,
   'font-size:28px;line-height:1.5;white-space:pre-wrap;max-width:1500px;'
   + 'margin:0 0 40px;word-break:break-word');
  appendText(panel, 'p', 'Please tell the household admin about this message.',
   'font-size:28px;line-height:1.6;margin:8px 0');
  appendText(panel, 'p', 'Press Back to exit.',
   'font-size:28px;line-height:1.6;margin:8px 0;color:#8fa3ad');
  document.body.appendChild(panel);
 }

 function handle(event) {
  if (fired) return; // first unhandled error wins; never twice
  fired = true;
  var message = describe(event.error || event.message || event.reason);
  var context = {};
  if (event.filename !== undefined) {
   context.source = utf8Truncate(String(event.filename || ''), MAX_SOURCE_BYTES);
   if (event.lineno) context.line = event.lineno;
   if (event.colno) context.column = event.colno;
  }
  render(message);
  report(message, context);
 }

 function isBackKey(event) {
  // Mirrors the app's remoteAction() normalization (navigation.ts):
  // keycode 10009, 'Back'/'XF86Back'. 'Return' is Select in the app's
  // normalization, so it must not exit the panel.
  return event.keyCode === 10009 || event.key === 'Back' || event.key === 'XF86Back';
 }

 function exit() {
  if (exitApp) {
   try {
    exitApp();
   } catch (error) { /* exiting must never crash again */ }
   return;
  }
  try {
   var application = window.tizen && window.tizen.application
    && window.tizen.application.getCurrentApplication
    && window.tizen.application.getCurrentApplication();
   if (application && typeof application.exit === 'function') application.exit();
  } catch (error) { /* no Tizen application API: nothing else to try */ }
 }

 function onKey(event) {
  if (!fired || !isBackKey(event)) return; // until the panel shows, the app owns Back
  if (typeof event.preventDefault === 'function') event.preventDefault();
  exit();
 }

 window.FileListFatalError = {
  install: function(options) {
   if (wired) return;
   wired = true;
   options = options || {};
   if (typeof options.endpoint === 'string' && options.endpoint) endpoint = options.endpoint;
   if (typeof options.onExit === 'function') exitApp = options.onExit;
   window.addEventListener('error', handle);
   window.addEventListener('unhandledrejection', handle);
   document.addEventListener('keydown', onKey);
  },
  _release: function() {
   window.removeEventListener('error', handle);
   window.removeEventListener('unhandledrejection', handle);
   document.removeEventListener('keydown', onKey);
  }
 };
}());

(function() {
 'use strict';
 // Android shell glue for the shared TV web app. This file, plus the page
 // above, are the only Android-specific bytes in the package; app.js/app.css
 // ship byte-identical to the Tizen WGT (spec: Parity contract rule 1).
 window.FileListTVIdentity = { name: 'Torrent TV', monogram: 'TT' };

 var native = window.FileListTVNative || null;
 var listener = null;
 var callbacks = {};

 window.FileListTVBridge = {
  // Entry point for Kotlin's evaluateJavascript callbacks: player events
  // route to the registered AVPlay listener, prepare results to their
  // tokens.
  dispatch: function(payload) {
   var event;
   try { event = JSON.parse(payload); } catch (error) { return; }
   if (event.kind === 'event' && listener && typeof listener[event.name] === 'function') {
    try { listener[event.name].apply(null, event.args || []); } catch (error) { }
    return;
   }
   if (event.kind === 'callback' && typeof callbacks[event.token] === 'function') {
    var callback = callbacks[event.token];
    delete callbacks[event.token];
    try { callback.apply(null, event.args || []); } catch (error) { }
   }
  }
 };

 function registerCallback(callback) {
  var token = 'cb' + Math.random().toString(36).slice(2);
  callbacks[token] = callback;
  return token;
 }

 // Same shape as Samsung's webapis.avplay: the Player component in app.js
 // programs against this API and must not know the difference.
 var avplay = {
  open: function(url) { native.open(String(url)); },
  setDisplayRect: function(x, y, w, h) { native.setDisplayRect(Number(x), Number(y), Number(w), Number(h)); },
  setDisplayMethod: function(mode) { native.setDisplayMethod(String(mode)); },
  setListener: function(value) { listener = value || null; },
  prepareAsync: function(onSuccess, onError) {
   native.prepareAsync(registerCallback(onSuccess), registerCallback(onError));
  },
  play: function() { native.play(); },
  pause: function() { native.pause(); },
  seekTo: function(ms) { native.seekTo(Number(ms)); },
  stop: function() { native.stop(); },
  close: function() { native.close(); },
  getDuration: function() { return Number(native.getDuration()) || 0; },
  getTotalTrackInfo: function() {
   try { return JSON.parse(native.getTotalTrackInfo()); } catch (error) { return []; }
  },
  setSelectTrack: function(type, index) { native.setSelectTrack(String(type), Number(index)); },
  // Server-prepared VTT subtitles render in the page's HTML overlay, and
  // the overlay applies its own delay, so these two are deliberate no-ops.
  setSilentSubtitle: function(value) { native.setSilentSubtitle(Boolean(value)); },
  setExternalSubtitlePath: function() { },
  setSubtitlePosition: function() { }
 };

 window.webapis = {
  avplay: avplay,
  network: {
   getIp: function() { return native ? String(native.getIp() || '') : ''; },
   getSubnetMask: function() { return native ? String(native.getSubnetMask() || '') : ''; }
  }
 };

 // Video plays on a native SurfaceView behind the WebView; while the player
 // is on screen the page must go transparent where the video should show
 // (the Tizen engine composites AVPlay inside the object element natively;
 // a WebView does not). Player mode is detected from the DOM, not from UI
 // code, so app.js stays platform-neutral.
 function watchPlayerMode() {
  var style = document.createElement('style');
  style.textContent = 'html.video-behind,html.video-behind body{background:transparent !important}' +
   'html.video-behind .player-shell{background:transparent !important}';
  document.head.appendChild(style);
  var observer = new MutationObserver(function() {
   document.documentElement.classList.toggle('video-behind', Boolean(document.querySelector('.player-shell')));
  });
  observer.observe(document.body, { childList: true, subtree: true });
 }
 if (document.body) watchPlayerMode();
 else document.addEventListener('DOMContentLoaded', watchPlayerMode);

 // D-pad evidence for the CI boot smoke: the page's focus engine moves
 // focus inside the WebView, which the Android view hierarchy cannot
 // see. A poll reports the focused control's data-focus-key to logcat
 // through the native bridge whenever it changes — event delivery proved
 // unreliable on the CI emulator, so the poll reads
 // document.activeElement directly instead of trusting focusin.
 var lastFocusKey = null;
 function reportFocus() {
  var target = document.activeElement;
  var key = target && target.getAttribute ? target.getAttribute('data-focus-key') : null;
  if (!key || key === lastFocusKey) return;
  lastFocusKey = key;
  var line = 'TVFOCUS ' + key;
  try { if (native && typeof native.log === 'function') native.log(line); } catch (error) { }
  console.log(line);
 }
 document.addEventListener('focusin', reportFocus);
 var ticks = 0;
 window.setInterval(function() {
  ticks += 1;
  // Bounded status heartbeat: if focus never lands on a control, these
  // lines say whether activeElement stays body — the smoke's logcat
  // evidence then answers why without another guessing round.
  if (ticks % 10 === 0 && ticks <= 100) {
   var active = document.activeElement;
   var tag = active && active.tagName ? active.tagName : 'none';
   var activeKey = active && active.getAttribute ? active.getAttribute('data-focus-key') : null;
   try { if (native && typeof native.log === 'function') native.log('TVBOOT tick ' + ticks + ' active=' + tag + ' key=' + activeKey); } catch (error) { }
  }
  reportFocus();
 }, 300);
 try { if (native && typeof native.log === 'function') native.log('TVBOOT bridge attached'); } catch (error) { }
 console.log('TVBOOT bridge attached');
}());

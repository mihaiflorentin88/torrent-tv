(function() {
  'use strict';

  // The fatal panel (ticket #80) owns every unhandled error from boot onward
  // and is installed before the app bundle so it still paints when the bundle
  // itself crashed. Unlike this boot handler it never stands down after first
  // render, so post-launch errors surface instead of being swallowed.
  if (window.FileListFatalError) window.FileListFatalError.install({ endpoint: '/api/v1/diagnostics/client' });

  // Engines older than Chromium 54 (such as Chromium 53 on webOS 4.x) lack
  // Object.getOwnPropertyDescriptors, which esbuild's object spread helper
  // reads at module-evaluation time before bundle imports can run. Installing
  // it here guarantees the method exists before app.js evaluates.
  if (!Object.getOwnPropertyDescriptors) {
    Object.getOwnPropertyDescriptors = function(object) {
      if (object === null || object === undefined) throw new TypeError('Cannot convert undefined or null to object');
      var keys = Object.getOwnPropertyNames(object);
      var descriptors = {};
      for (var i = 0; i < keys.length; i++) {
        descriptors[keys[i]] = Object.getOwnPropertyDescriptor(object, keys[i]);
      }
      if (Object.getOwnPropertySymbols) {
        var symbols = Object.getOwnPropertySymbols(object);
        for (var j = 0; j < symbols.length; j++) {
          descriptors[symbols[j]] = Object.getOwnPropertyDescriptor(object, symbols[j]);
        }
      }
      return descriptors;
    };
  }

  var ready = false;
  var stage = 'Loading application bundle';

  function messageElement() {
    return document.getElementById('startup-message');
  }

  function describe(value) {
    if (!value) return 'Unknown error';
    if (value.message) return value.message;
    return String(value);
  }

  function show(message, failed) {
    var element = messageElement();
    if (!element) return;
    element.textContent = message;
    if (failed) element.style.color = '#ff9b9b';
  }

  window.FileListBoot = {
    stage: function(value) {
      stage = value;
      show(value + '…', false);
    },
    ready: function() {
      ready = true;
      var startup = document.getElementById('startup');
      if (startup && startup.parentNode) startup.parentNode.removeChild(startup);
    },
    fail: function(error) {
      ready = true;
      show('Application startup failed\n\n' + describe(error), true);
    }
  };

  window.addEventListener('error', function(event) {
    var location = event.filename ? '\n' + event.filename + ':' + event.lineno + ':' + event.colno : '';
    window.FileListBoot.fail(describe(event.error || event.message) + location);
  });

  window.addEventListener('unhandledrejection', function(event) {
    window.FileListBoot.fail(event.reason);
  });

  window.setTimeout(function() {
    if (!ready) show(stage + ' is taking longer than expected.', false);
  }, 8000);
}());

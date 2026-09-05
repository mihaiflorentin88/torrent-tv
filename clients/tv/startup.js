(function() {
  'use strict';

  // The fatal panel (ticket #80) owns every unhandled error from boot onward
  // and is installed before the app bundle so it still paints when the bundle
  // itself crashed. Unlike this boot handler it never stands down after first
  // render, so post-launch errors surface instead of being swallowed.
  if (window.FileListFatalError) window.FileListFatalError.install({ endpoint: '/api/v1/diagnostics/client' });

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

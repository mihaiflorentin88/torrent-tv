package gui

import "errors"

// ErrNoDisplay is returned by Run when no graphics session is available:
// a bare launch must exit with a pointer at `serve`, never a raw GTK or
// webkit error. It lives outside the build-tagged files so both the Wails
// runner and the linux/arm fallback return the same sentinel.
var ErrNoDisplay = errors.New("no display available for the GUI; run 'torrent-tv serve' instead")

# Torrent TV design system

## Direction

The product is a private screening archive: Plex-first information density and source transparency, with Netflix-inspired cinematic artwork and restrained reveal. It is not a trademark clone. Static artwork, typography, and selection state carry the experience; autoplay video backgrounds are intentionally excluded.

## Visual language

- Base: near-black charcoal `#090d10`; panels `#11181d`; raised controls `#1a242b`.
- Accent: sea-glass teal `#55dfc1`; TV focus adds a white inner edge and teal outer ring.
- Text: cool white `#f4f7f8`; secondary copy `#a8b3b9`.
- Type: platform/system sans (Arial/Helvetica on Tizen) for reliable offline rendering and predictable TV metrics.
- Corners: compact 8–12 px. Depth comes from tonal separation and artwork shading, not glassmorphism.
- Motion: short 120–200 ms focus/sidebar transitions; honor reduced motion on the browser. No decorative looping animation.

## Information architecture

Home prioritizes Continue Watching, then discovery. The left rail groups Home/Search, My Library, Tracker, and settings/jobs. Canonical titles open into movie versions or show → season → episode → source. Source rows always expose resolution, source/codec where known, size, and seeders.

## TV interaction

The sidebar is compact until focus enters it. Left from content column zero opens it; Right restores the exact prior content control and collapses it. Rows and columns are explicit, vertical movement clamps to the nearest available column, and Back unwinds detail/dialog/player layers before exiting.

Player Left/Right on a hidden overlay reveals and focuses the timeline while seeking ±10 seconds. Timeline Left/Right continues seeking without moving focus. Up selects the timeline; Down returns to the remembered toolbar control. Dialogs remember and restore their launcher.

## References and artifacts

Approved design references are stored under `.impeccable/mocks/`; they use fictional artwork and are visual direction only. `PRODUCT.md` records the household/profile and TV-settings product decisions. Browser markup includes the direction contract used for design review.

# User guide

## First run

Start the standalone server and open its address in a browser, normally `http://server.lan:8097` on the private LAN. Open **Settings** and enter the FileList username/passkey and the optional TMDB API key on the **Tracker** tab, the qBittorrent Web UI address, credentials, and download root on **Storage**, and the optional SubDL URL and key beside the subtitle settings on **Playback**. Generate the free SubDL key from the API section of `https://subdl.com/panel`. One **Save changes** button on the sticky bar at the bottom of the page saves the whole settings object, and the **Test** tab runs all five connection tests in one place. The server stores configuration in the file shown at the top of Settings; `.env` is only a developer test aid.

The service has no client login in release 1. Keep it on trusted private CIDRs and do not port-forward it.

## Browse and choose a source

Home begins with Continue Watching, followed by discovery rows. **My Library** separates Continue Watching, Favorites, Watched, Downloads, and a mixed dashboard. **Tracker** provides a dashboard, Browse, Recently Added, Categories, live tracker-backed search, filters, and sorting. Every dashboard and non-Downloads household rail shows one card per canonical TV series, even when several episodes were watched or downloaded; the card opens the complete show. Downloads deliberately remains file-level. The browser rail stays visible; the TV rail collapses after Right returns to content.

Releases are grouped into a canonical movie or show title. A show opens as seasons, episodes, then source versions. A movie shows its versions directly. Each version exposes resolution/source/codec, byte size in B/KB/MB/GB/TB, and seeders so you can make the final choice.

For a complete-season release, choose the exact release card and select **Download season** once. Each pack card reports its own queued/downloading/partial/downloaded state; a downloaded release stays visibly marked while other quality/source alternatives remain available. The server selects every playable episode inside that torrent and keeps the series page open; each episode tile shows its own filename, size, progress, and Play action. Downloads also exposes the individual `SxxExx` files for transfer management. Those rows are different files in one shared torrent, not duplicate transfers. Pause, Resume, and Delete still operate on the shared torrent; the one Delete confirmation removes the torrent and all of its files. A legacy duplicate row pointing at the same torrent file is collapsed during reconciliation.

## Playback and downloads

Starting a source first reuses an exact managed download and only contacts FileList when no matching release/file exists. This means a FileList rate limit cannot block playback of media that remains in Downloads. Completed files also play when qBittorrent is unavailable. Incomplete items show **Progressive stream** and begin as soon as requested pieces are readable; completed items show **Downloaded file**. Downloads refreshes live while it is open and provides search plus status filtering and sorting. The page never adopts unrelated qBittorrent torrents. Its single **Delete download** action removes the torrent and permanently deletes incomplete and completed files after confirmation.

Playback history survives torrent removal. Browser and TV resume state, watched state, favorites, and recent items are server-backed and shared. A previously watched series exposes **Resume episode** on its title page. Reaching the end of an episode automatically prepares and starts the immediate next episode—even when playback began at episode 3—and stops after the last known episode.

Both players remember audio and subtitle choices for each file. Unless changed, audio prefers English and subtitles try Romanian first, then English. If neither language is present locally, the server searches for an English subtitle, converts it to WebVTT, stores it in the subtitle cache, and reuses the same prepared asset later. Choosing another track or **Off** overrides automatic selection for that file. The browser's custom player always lists the original audio tracks: codecs the browser supports natively play the single progressive stream directly, while AC3/EAC3/DTS-class audio plays from a compatibility stream that transcodes only the selected track to AAC stereo and copies the video (see `docs/adr/0003`). Its timeline uses the original file duration, so switching tracks or seeking cannot move the total time or progress marker. The TV uses AVPlay's native audio tracks and the original file. The TV keeps rendering downloaded WebVTT cues in its dedicated overlay, avoiding AVPlay external-file inconsistencies.

My Library cards use their cached canonical title, artwork, year/resolution when available, watched or resume state, and the exact selected episode. Selecting a card opens canonical details with that episode highlighted; playback starts only from **Play**, **Resume**, or **Play and download**. Categories groups only downloaded, watched, in-progress, recently viewed, or favorited media.

Settings provides a `?` control beside each field. Hover for a short explanation or select it to open copyable instructions. One **Save changes** button on the sticky bar at the bottom of the page saves the whole settings object; the provider-specific Test buttons live together on the **Test** tab for live diagnostics. The **Maintenance** tab shows observed catalog coverage above the **Fetch latest** and **Rebuild catalog** cards: **Fetch latest** appends or updates the newest tracker records, and **Rebuild catalog** refreshes each category's API-visible window and reconstructs projections without deleting an older observation, asking for confirmation first. Search contacts FileList only after **Search** is selected. The screen first shows cache matches, then refreshes after the persistent search job permanently grows the cache and queues version/episode discovery. Typing, filtering, sorting, paging, opening details, and visiting Settings/Events/Jobs remain cache-only. Both maintenance actions create visible Jobs entries and are available under Events.

Progressive Range playback is verified on the Raspberry Pi while qBittorrent is incomplete. Startup can pause while the exact pieces arrive; browser and Tizen players show live progress and retry the stream automatically rather than waiting for the whole torrent. Physical-TV AVPlay verification below 100% on both Verified TVs remains tracked in [Known issues](KNOWN_ISSUES.md).

## Samsung TV

The TV client is one package spanning every Samsung Tizen platform from the Support floor, Tizen 5.0, through the latest. Behavior is verified on the household's two Verified TVs — a 2019 premium Tizen 5.0 set and the 2023 S90C; any other Tizen 5.0-or-newer set is best-effort. Build `clients/tizen/.build/artifacts/torrent-tv-0.3.0.wgt` with `make frontend`, then install that same unsigned file on either TV through Apps2Samsung, which signs it for the selected TV. On first launch, select a validated server found on the local network or choose **Manual address**. A successful choice is saved and reused on later launches; failed connection attempts never replace it. See [TIZEN.md](TIZEN.md) for Developer Mode, signing, compatibility, and the physical-TV verification log.

In the pending-TV-test 0.3.0 build, the player toolbar ends with a **Hide** button that dismisses the controls immediately; while the controls are hidden, any recognized remote key restores them first, and Left/Right then reveal and focus the timeline and seek ten seconds. Repeated Left/Right remains on the timeline; toolbar Left/Right follows the physical button row, Up reaches the timeline, Down restores the remembered toolbar control, and vertical menus use only Up/Down. Complete-season cards are disclosures: OK expands a card, and downloading starts only after focusing and selecting its inner **Download** button. Media keys work independently. Short Back closes the active dialog/player layer or toggles the main sidebar; hold Back for five seconds to exit. Record the physical result in the Tizen log after installation.

## Troubleshooting

- **qBittorrent torrent not found:** the server reconciles a missing owned torrent and removes the stale managed-download row. Refresh Downloads.
- **Playback fails after download:** Tizen is direct play. The server transcodes browser-hostile audio to AAC stereo, but the video codec must still be supported by the browser; choose another video codec when necessary.
- **No artwork:** configure TMDB; parsed names and generated placeholders remain usable without it.
- **No results:** enter at least three characters and select **Search**. That explicit action queries FileList and stores every returned release. Zero-seeder releases remain cached but are hidden from discovery.
- **Subtitle provider error:** open Settings → Playback, verify `https://api.subdl.com`, save a SubDL API key, then run **Test SubDL** on the **Test** tab. The error includes the provider response without exposing the key. Archive payloads are rejected because this integration intentionally accepts only direct subtitle files.
- **Failed background work:** open Jobs, search by title or job ID, and inspect Details for provider, phase, attempt, wait, and error context. Retry is available for any terminal job. Rate-limited jobs resume when the provider reset is due; other transient failures are retried hourly.
- **TV cannot connect:** verify the TV and Pi are on the same LAN and the TV server address includes `http://` and the port.
## Canonical series and download markers

Selecting media in My Library or Tracker opens its details. A series item recorded from an individual episode opens the complete series page, selects its season, and expands that episode. Selecting an episode shows its versions; it does not start a download.

- **Play** uses an already managed local or progressive source.
- **Play and download** adds an unmanaged source and starts playback when its opening pieces are available.
- **Resume SxxExx** replaces the primary Play action when a series has unfinished playback. It selects the newest unfinished episode and continues from its saved position; movies use **Resume**.
- A partial marker on a season means at least one episode is managed but the whole season is not downloaded.
- A completed download marker means every known episode has at least one completed version.
- Watch markers are calculated independently per episode and per season.

Starting a complete-season pack leaves the series page open. As the pack is registered, each playable file appears under its matching episode and can be played independently.

The Downloads page refreshes progress, speed, peers, and state in place. Existing tiles do not reorder or scroll the page. Every tile expands enough to show its selected-file size/index and complete-torrent size/seeders without clipping. A genuinely new download can appear while the page is open; if you are reading farther down, the current visible tile remains anchored.

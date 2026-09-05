package httpapi

import (
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

// SchemaField describes one editable setting for the settings UI. The JSON
// shape is the /api/v1/settings/schema contract, shared by the webapp and
// the desktop bindings.
type SchemaField struct {
	Key             string `json:"key"`
	Label           string `json:"label"`
	Help            string `json:"help"`
	Obtain          string `json:"obtain,omitempty"`
	TVVisible       bool   `json:"tvVisible"`
	Sensitive       bool   `json:"sensitive"`
	RestartRequired bool   `json:"restartRequired"`
	ReadOnly        bool   `json:"readOnly"`
}

// SettingsView is the GET /api/v1/settings body: the settings with the five
// secrets blanked, one Configured flag per secret, and the settings file
// path. The HTTP handler and the desktop bindings both serve this shape.
type SettingsView struct {
	config.Settings
	FileListPasskeyConfigured     bool   `json:"fileListPasskeyConfigured"`
	QBittorrentPasswordConfigured bool   `json:"qbittorrentPasswordConfigured"`
	TMDBAPIKeyConfigured          bool   `json:"tmdbApiKeyConfigured"`
	SubDLAPIKeyConfigured         bool   `json:"subDLApiKeyConfigured"`
	PortalAPIKeyConfigured        bool   `json:"portalAPIKeyConfigured"`
	SettingsPath                  string `json:"settingsPath"`
}

// RedactedSettings builds the view: secrets are blanked in the payload and
// surfaced only as Configured flags, so responses never carry credentials.
func RedactedSettings(v config.Settings, path string) SettingsView {
	view := SettingsView{Settings: v, FileListPasskeyConfigured: v.FileListPasskey != "", QBittorrentPasswordConfigured: v.QBittorrentPassword != "", TMDBAPIKeyConfigured: v.TMDBAPIKey != "", SubDLAPIKeyConfigured: v.SubDLAPIKey != "", PortalAPIKeyConfigured: v.PortalAPIKey != "", SettingsPath: path}
	view.Settings.FileListPasskey = ""
	view.Settings.QBittorrentPassword = ""
	view.Settings.TMDBAPIKey = ""
	view.Settings.SubDLAPIKey = ""
	view.Settings.PortalAPIKey = ""
	return view
}

// SettingsSchema returns the schema field list, marking keys managed by the
// process environment read-only. The HTTP handler and the desktop bindings
// both serve this.
func SettingsSchema(s *config.Store) []SchemaField {
	fields := []SchemaField{
		{Key: "instanceName", Label: "Server name", Help: "Friendly name shown when a television discovers this server on the local network."},
		{Key: "fileListUrl", Label: "FileList URL", Help: "Address of the private tracker API. The default works unless FileList changes domain.", TVVisible: false},
		{Key: "fileListUsername", Label: "FileList username", Help: "Account name used with your passkey for API requests.", Obtain: "Sign in at https://filelist.io and use the username shown on your profile.", Sensitive: true},
		{Key: "fileListPasskey", Label: "FileList passkey", Help: "Private API credential used to search and download torrent metadata. Treat it like a password.", Obtain: "Sign in at https://filelist.io, open your profile page, and copy the passkey shown there — never your login password.", Sensitive: true},
		{Key: "tmdbApiKey", Label: "TMDB API key or token", Help: "Adds posters, backdrops, descriptions, years and ratings.", Obtain: "Create a free account at https://www.themoviedb.org/signup, then request a key at https://www.themoviedb.org/settings/api. The v3 key or the v4 Read Access Token both work.", Sensitive: true},
		{Key: "qbittorrentUrl", Label: "qBittorrent URL", Help: "Address of qBittorrent Web UI used to add and manage this app's downloads. Only used by the optional qBittorrent engine.", Obtain: "Install qBittorrent from https://www.qbittorrent.org and enable its Web UI under Tools → Options → Web UI."},
		{Key: "qbittorrentUsername", Label: "qBittorrent username", Help: "Username configured in qBittorrent Web UI authentication.", Obtain: "Set it in qBittorrent under Tools → Options → Web UI → Authentication.", Sensitive: true},
		{Key: "qbittorrentPassword", Label: "qBittorrent password", Help: "Password configured in qBittorrent Web UI authentication.", Obtain: "Set it in qBittorrent under Tools → Options → Web UI → Authentication.", Sensitive: true},
		{Key: "downloadEngine", Label: "Download engine", Help: "Selects how downloads are acquired: the built-in torrent engine (default) or the external qBittorrent Web UI. Changing it requires restart.", RestartRequired: true},
		{Key: "torrentPeerPort", Label: "Torrent peer port", Help: "Port the built-in engine listens on for peer connections. Changing it requires restart.", Obtain: "A fixed port improves seeding reachability; forward it on your router only if you want inbound peers.", RestartRequired: true},
		{Key: "torrentSessionDir", Label: "Torrent session directory", Help: "Directory where the built-in engine keeps its fast-resume session state. Changing it requires restart.", RestartRequired: true},
		{Key: "downloadRoot", Label: "Download root", Help: "Server filesystem path where downloads are stored. The built-in engine writes here directly; it must be writable by the server."},
		{Key: "allocationGb", Label: "Allocation (GB)", Help: "Total stored torrent content the service keeps, in binary gigabytes (GiB); fractional values allowed, 0 disables retention."},
		{Key: "reserveGb", Label: "Free-space reserve (GB)", Help: "Free space kept free on the download volume, in binary gigabytes (GiB); 0 disables the reserve check."},
		{Key: "evictionRules", Label: "Eviction rules", Help: "Comma-separated eviction order, tried left to right: oldest-completed, newest-completed, least-recently-played, most-recently-played, watched-first, never-watched-first, largest, smallest. Recency counts the download's last activity, so streaming refreshes it; ties break to the oldest completed download and an empty list restores oldest-completed."},
		{Key: "protectIncomplete", Label: "Protect incomplete downloads", Help: "Keeps downloads that are still fetching out of eviction. Off, an unfinished download can be deleted when storage runs low."},
		{Key: "protectLeased", Label: "Protect actively streamed downloads", Help: "Keeps downloads with an active or recent stream out of eviction. Off, the torrent behind a stream can be deleted when storage runs low."},
		{Key: "protectFavorites", Label: "Protect favorites", Help: "Keeps downloads whose canonical title is in the household favorites out of eviction."},
		{Key: "protectNeverWatched", Label: "Protect never-watched downloads", Help: "Keeps downloads the household has never finished watching out of eviction."},
		{Key: "subDLUrl", Label: "SubDL API URL", Help: "Official SubDL API base used for direct subtitle files.", Obtain: "Use https://api.subdl.com.", Sensitive: false},
		{Key: "subDLApiKey", Label: "SubDL API key", Help: "Free SubDL API credential used to search and download direct subtitle files.", Obtain: "Create a free account and generate a key in the API section at https://subdl.com/panel.", Sensitive: true},
		{Key: "portalAPIKey", Label: "Supporter API key", Help: "Optional supporter credential that unlocks the supporter-only sections of the catalog. Stored like a password.", Sensitive: true},
		{Key: "subtitleCachePath", Label: "Subtitle cache path", Help: "Server directory containing prepared WebVTT and SAMI subtitle files."},
		{Key: "subtitleCacheMaxBytes", Label: "Subtitle cache maximum bytes", Help: "Maximum disk space used by prepared and downloaded subtitle files."},
		{Key: "ffprobePath", Label: "ffprobe path", Help: "Absolute path to ffprobe. It reads embedded subtitle language, title, codec and disposition metadata without transcoding.", Obtain: "Detected automatically on PATH at first start. Install FFmpeg with brew install ffmpeg (macOS), apt/dnf install ffmpeg (Linux), or winget install Gyan.FFmpeg (Windows); set a path manually only if auto-detection fails."},
		{Key: "ffmpegPath", Label: "FFmpeg path", Help: "Absolute path to FFmpeg. It extracts embedded subtitles from the original media without transcoding. Tizen remains direct-play.", Obtain: "Detected automatically on PATH at first start. Install FFmpeg with brew install ffmpeg (macOS), apt/dnf install ffmpeg (Linux), or winget install Gyan.FFmpeg (Windows); set a path manually only if auto-detection fails."},
		{Key: "preferredSubtitleLanguage", Label: "Preferred subtitle language", Help: "ISO language code selected first for automatic subtitles, for example ro.", TVVisible: true},
		{Key: "fallbackSubtitleLanguage", Label: "Fallback subtitle language", Help: "Language used when no suitable preferred-language subtitle exists.", TVVisible: true},
		{Key: "preferredAudioLanguage", Label: "Preferred audio language", Help: "ISO language code selected first when a media file contains multiple audio tracks, for example en.", TVVisible: true},
		{Key: "initialBufferBytes", Label: "Initial buffer bytes", Help: "Amount probed for media details before an in-progress download answers the info request. Larger values improve reliability but delay startup.", TVVisible: true},
		{Key: "streamStartBytes", Label: "Stream start bytes", Help: "Leading slice that must be readable before a stream responds while a download is in progress. Small values start playback sooner on slow swarms.", TVVisible: true},
		{Key: "readAheadBytes", Label: "Read-ahead bytes", Help: "Range prioritized ahead of the current playback position.", TVVisible: true},
		{Key: "pieceWaitTimeoutSeconds", Label: "Piece timeout seconds", Help: "Maximum wait for missing pieces before a stream request fails.", TVVisible: true},
		{Key: "watchedThresholdPercent", Label: "Watched threshold percent", Help: "Playback percentage at which an item moves to Watched.", TVVisible: true},
		{Key: "maxConcurrentJobs", Label: "Maximum concurrent jobs", Help: "Global ceiling for background work. FileList requests remain serialized.", TVVisible: true, RestartRequired: true},
		{Key: "titleRefreshTimeoutMinutes", Label: "Title refresh timeout minutes", Help: "Active execution allowance after a title refresh obtains its worker slots. Queue and rate-limit waiting do not count.", TVVisible: true, RestartRequired: true},
		{Key: "listenAddress", Label: "Listen address", Help: "Network address and port used by the server. Changing it requires restart.", RestartRequired: true},
		{Key: "databasePath", Label: "Database path", Help: "SQLite catalog and household-state file. Changing it requires restart.", RestartRequired: true},
		{Key: "trustedCidrs", Label: "Trusted CIDRs", Help: "Private network ranges allowed to use the unauthenticated server."},
	}
	for i := range fields {
		fields[i].ReadOnly = s.EnvironmentManaged(fields[i].Key)
		if fields[i].ReadOnly {
			fields[i].Help += " This value is managed by the server environment and is read-only here."
		}
	}
	return fields
}

package domain

import (
	"errors"
	"fmt"
	"time"
)

var ErrTorrentNotFound = errors.New("torrent not found in the active engine")

// ErrTorrentRemoved reports that the tracker no longer hosts a catalogued
// release: FileList answers its download endpoint with an HTML error page
// saying it cannot find the .torrent file.
var ErrTorrentRemoved = errors.New("release removed from FileList")

// AllocationError rejects a download that cannot fit the Allocation even
// after evicting every unprotected torrent — the starvation path of
// ADR-0004. The HTTP layer maps it to a 409 problem; Error names the
// exhausted Allocation and the space required so both clients can show an
// actionable message.
type AllocationError struct {
	Release       string
	RequiredBytes int64 // the incoming torrent's size
	FreeBytes     int64 // room left under the Allocation once protected downloads are honored
	CapacityBytes int64 // the configured Allocation, in bytes
}

func (e *AllocationError) Error() string {
	gib := func(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }
	return fmt.Sprintf("the storage Allocation (%s) is exhausted: %q needs %s and only %s remains after protecting existing downloads — remove downloads or raise allocationGb", gib(e.CapacityBytes), e.Release, gib(e.RequiredBytes), gib(max(e.FreeBytes, 0)))
}

type TorrentRelease struct {
	ID                string     `json:"id"`
	TrackerID         string     `json:"trackerId"`
	TrackerName       string     `json:"trackerName"`
	ProviderID        string     `json:"providerId"`
	CategoryID        string     `json:"categoryId"`
	BrowseClass       string     `json:"browseClass"`
	DiscoveryExcluded bool       `json:"-"`
	Name              string     `json:"name"`
	Category          string     `json:"category"`
	SizeBytes         int64      `json:"sizeBytes"`
	IMDbID            string     `json:"imdbId,omitempty"`
	Seeders           int        `json:"seeders"`
	Leechers          int        `json:"leechers"`
	TimesCompleted    int        `json:"timesCompleted"`
	Freeleech         bool       `json:"freeleech"`
	DoubleUp          bool       `json:"doubleUp"`
	Internal          bool       `json:"internal"`
	Moderated         bool       `json:"moderated"`
	SmallDescription  string     `json:"smallDescription,omitempty"`
	UploadedAt        *time.Time `json:"uploadedAt,omitempty"`
	FileCount         int        `json:"fileCount"`
	Comments          int        `json:"comments"`
}
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
	Total      int     `json:"total"`
	Stale      bool    `json:"stale,omitempty"`
}
type TorrentFile struct {
	Index     int     `json:"index"`
	Path      string  `json:"path"`
	SizeBytes int64   `json:"sizeBytes"`
	Progress  float64 `json:"progress"`
	Priority  int     `json:"priority"`
	Offset    int64   `json:"offset"`
	Playable  bool    `json:"playable"`
}
type TrackerStatus struct {
	URL     string `json:"url"`
	Status  int    `json:"status"`
	Message string `json:"message,omitempty"`
	Peers   int    `json:"peers"`
	Seeds   int    `json:"seeds"`
}
type DownloadStatus struct {
	Hash                                                         string  `json:"hash"`
	State                                                        string  `json:"state"`
	Progress                                                     float64 `json:"progress"`
	DownloadedBytes, TotalBytes, SpeedBytesPerSecond, ETASeconds int64
	Peers, Seeds                                                 int
	UploadSpeedBytesPerSecond                                    int64
	PieceSize                                                    int64
	Sequential, FirstLastPriority                                bool
	SavePath, ContentPath, TempPath                              string
	TempPathEnabled                                              bool
	Trackers                                                     []TrackerStatus
	TrackerError                                                 string
}
type Download struct {
	ID, ReleaseID, EngineID, FilePath, AbsolutePath, State                                                              string
	TrackerID, TrackerName                                                                                              string
	TitleID, DisplayTitle, ReleaseName, Category                                                                        string
	Parsed                                                                                                              ParsedRelease
	FileIndex                                                                                                           int
	SizeBytes, ReleaseSizeBytes, FileOffset, PieceSize, BufferedBytes, DownloadedBytes, SpeedBytesPerSecond, ETASeconds int64
	Peers, Seeds, TrackerSeeders                                                                                        int
	UploadSpeedBytesPerSecond                                                                                           int64
	Rating                                                                                                              float64
	RatingVotes                                                                                                         int
	RatingProvider                                                                                                      string
	Progress                                                                                                            float64
	Leased                                                                                                              bool
	CreatedAt, UpdatedAt                                                                                                time.Time
	Error                                                                                                               string
}
type PieceMap struct {
	States    []int
	PieceSize int64
}

type Job struct {
	ID            string     `json:"id"`
	TrackerID     string     `json:"trackerId,omitempty"`
	Kind          string     `json:"kind"`
	State         string     `json:"state"`
	Label         string     `json:"label"`
	DedupeKey     string     `json:"dedupeKey"`
	Progress      float64    `json:"progress"`
	Attempt       int        `json:"attempt"`
	Error         string     `json:"error,omitempty"`
	Retryable     bool       `json:"retryable"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type JobLog struct {
	ID        int64          `json:"id"`
	JobID     string         `json:"jobId"`
	Attempt   int            `json:"attempt"`
	Level     string         `json:"level"`
	Phase     string         `json:"phase"`
	Message   string         `json:"message"`
	Context   map[string]any `json:"context,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

type Event struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"createdAt"`
}

type PlaybackState struct {
	ProfileID  string    `json:"profileId"`
	SourceID   string    `json:"sourceId"`
	ReleaseID  string    `json:"releaseId"`
	FileIndex  int       `json:"fileIndex"`
	FilePath   string    `json:"filePath"`
	PositionMS int64     `json:"positionMs"`
	DurationMS int64     `json:"durationMs"`
	Watched    bool      `json:"watched"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type PlaybackPreferences struct {
	ProfileID           string    `json:"profileId"`
	SourceID            string    `json:"sourceId"`
	AudioLanguage       string    `json:"audioLanguage"`
	AudioTrackIndex     int       `json:"audioTrackIndex"`
	SubtitleLanguage    string    `json:"subtitleLanguage"`
	SubtitleProvider    string    `json:"subtitleProvider,omitempty"`
	SubtitleCandidateID string    `json:"subtitleCandidateId,omitempty"`
	SubtitleMode        string    `json:"subtitleMode"`
	UpdatedAt           time.Time `json:"updatedAt,omitempty"`
}

type Favorite struct {
	ProfileID string    `json:"profileId"`
	TitleID   string    `json:"titleId"`
	ReleaseID string    `json:"releaseId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type HouseholdItem struct {
	Release TorrentRelease `json:"release"`
	Catalog *CatalogTitle  `json:"catalog,omitempty"`
	PlaybackState
	Favorite      bool   `json:"favorite"`
	TitleID       string `json:"titleId,omitempty"`
	SeasonNumber  int    `json:"seasonNumber,omitempty"`
	EpisodeNumber int    `json:"episodeNumber,omitempty"`
}

type HouseholdState struct {
	Favorites        []HouseholdItem `json:"favorites"`
	ContinueWatching []HouseholdItem `json:"continueWatching"`
	Recent           []HouseholdItem `json:"recent"`
	Watched          []HouseholdItem `json:"watched"`
}

type LibraryCategory struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type SubtitleCandidate struct {
	ID              string  `json:"id"`
	Provider        string  `json:"provider"`
	ProviderLabel   string  `json:"providerLabel,omitempty"`
	Language        string  `json:"language"`
	Title           string  `json:"title"`
	FileName        string  `json:"fileName,omitempty"`
	ReleaseName     string  `json:"releaseName,omitempty"`
	Format          string  `json:"format,omitempty"`
	Uploader        string  `json:"uploader,omitempty"`
	HearingImpaired bool    `json:"hearingImpaired,omitempty"`
	Description     string  `json:"description,omitempty"`
	Score           float64 `json:"score"`
	Cached          bool    `json:"cached"`
}

type SubtitleProviderWarning struct {
	Provider string `json:"provider"`
	Message  string `json:"message"`
}

type TorrentManifest struct {
	ReleaseID string        `json:"releaseId"`
	Files     []TorrentFile `json:"files"`
	Metainfo  []byte        `json:"-"`
	FetchedAt time.Time     `json:"fetchedAt"`
}

type SubtitleAsset struct {
	ID          string    `json:"id"`
	SourceID    string    `json:"sourceId,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	CandidateID string    `json:"candidateId,omitempty"`
	Name        string    `json:"name,omitempty"`
	Language    string    `json:"language"`
	URL         string    `json:"url"`
	Format      string    `json:"format"`
	MimeType    string    `json:"mimeType"`
	Path        string    `json:"-"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
	LastUsedAt  time.Time `json:"lastUsedAt,omitempty"`
}

type MediaSubtitleTrack struct {
	Index           int       `json:"index"`
	Language        string    `json:"language,omitempty"`
	Title           string    `json:"title,omitempty"`
	Codec           string    `json:"codec,omitempty"`
	Default         bool      `json:"default,omitempty"`
	Forced          bool      `json:"forced,omitempty"`
	HearingImpaired bool      `json:"hearingImpaired,omitempty"`
	ProbedAt        time.Time `json:"probedAt,omitempty"`
}

// MediaInfo describes the original selected media file. Its duration and
// stream indexes must never be derived from a generated compatibility stream,
// whose fragmented container grows while FFmpeg is running.
type MediaInfo struct {
	DurationMS  int64             `json:"durationMs"`
	AudioTracks []MediaAudioTrack `json:"audioTracks"`
	ProbedAt    time.Time         `json:"probedAt,omitempty"`
}

type MediaAudioTrack struct {
	Index    int    `json:"streamIndex"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Codec    string `json:"codec,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

// AudioSpan is the measured audio content of one probe window, taken from
// the same concatenated probe artifact (container head plus fetch window)
// that the decoder consumes. Packets are attributed to the fetch window by
// their byte position in the artifact (pos >= head length); all values are
// measured facts from packet data, never bitrate arithmetic (ADR-0002).
type AudioSpan struct {
	StreamIndex int   `json:"streamIndex"`
	StartByte   int64 `json:"startByte"`
	LengthBytes int64 `json:"lengthBytes"`
	FirstPTSMS  int64 `json:"firstPtsMs"`
	LastPTSMS   int64 `json:"lastPtsMs"`
}

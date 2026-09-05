package config

import (
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultSettingsPath = "data/settings.json"
	EnvironmentPrefix   = "TORRENT_TV_"
)

type Settings struct {
	InstanceName               string   `json:"instanceName"`
	ListenAddress              string   `json:"listenAddress"`
	TrustedCIDRs               []string `json:"trustedCidrs"`
	DatabasePath               string   `json:"databasePath"`
	DownloadRoot               string   `json:"downloadRoot"`
	FileListURL                string   `json:"fileListUrl"`
	FileListUsername           string   `json:"fileListUsername"`
	FileListPasskey            string   `json:"fileListPasskey,omitempty"`
	QBittorrentURL             string   `json:"qbittorrentUrl"`
	QBittorrentUsername        string   `json:"qbittorrentUsername"`
	QBittorrentPassword        string   `json:"qbittorrentPassword,omitempty"`
	DownloadEngine             string   `json:"downloadEngine"`
	TorrentPeerPort            int      `json:"torrentPeerPort"`
	TorrentSessionDir          string   `json:"torrentSessionDir"`
	InitialBufferBytes         int64    `json:"initialBufferBytes"`
	ReadAheadBytes             int64    `json:"readAheadBytes"`
	PieceWaitTimeoutSeconds    int      `json:"pieceWaitTimeoutSeconds"`
	StreamStartBytes           int64    `json:"streamStartBytes"`
	CatalogMaxAgeHours         int      `json:"catalogMaxAgeHours"`
	AllocationGB               float64  `json:"allocationGb"`
	ReserveGB                  float64  `json:"reserveGb"`
	EvictionRules              []string `json:"evictionRules"`
	ProtectIncomplete          bool     `json:"protectIncomplete"`
	ProtectLeased              bool     `json:"protectLeased"`
	ProtectFavorites           bool     `json:"protectFavorites"`
	ProtectNeverWatched        bool     `json:"protectNeverWatched"`
	PreferredSubtitleLanguage  string   `json:"preferredSubtitleLanguage"`
	FallbackSubtitleLanguage   string   `json:"fallbackSubtitleLanguage"`
	PreferredAudioLanguage     string   `json:"preferredAudioLanguage"`
	TMDBAPIKey                 string   `json:"tmdbApiKey,omitempty"`
	MetadataLanguage           string   `json:"metadataLanguage"`
	MetadataFallbackLanguage   string   `json:"metadataFallbackLanguage"`
	ArtworkCachePath           string   `json:"artworkCachePath"`
	ArtworkCacheMaxBytes       int64    `json:"artworkCacheMaxBytes"`
	SubDLURL                   string   `json:"subDLUrl"`
	SubDLAPIKey                string   `json:"subDLApiKey,omitempty"`
	SubtitleCachePath          string   `json:"subtitleCachePath"`
	SubtitleCacheMaxBytes      int64    `json:"subtitleCacheMaxBytes"`
	FFprobePath                string   `json:"ffprobePath"`
	FFmpegPath                 string   `json:"ffmpegPath"`
	WatchedThresholdPercent    int      `json:"watchedThresholdPercent"`
	MaxConcurrentJobs          int      `json:"maxConcurrentJobs"`
	TitleRefreshTimeoutMinutes int      `json:"titleRefreshTimeoutMinutes"`
	PortalAPIKey               string   `json:"portalAPIKey,omitempty"`
}

func Defaults() Settings {
	return Settings{
		InstanceName:  "Torrent TV",
		ListenAddress: ":8097", TrustedCIDRs: []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}, DatabasePath: "data/filelist.db",
		DownloadRoot: "data/downloads", FileListURL: "https://filelist.io", QBittorrentURL: "http://127.0.0.1:8080", DownloadEngine: "native", TorrentPeerPort: 42069, TorrentSessionDir: "data/torrent-session",
		InitialBufferBytes: 128 << 20, ReadAheadBytes: 256 << 20, PieceWaitTimeoutSeconds: 600, StreamStartBytes: 2 << 20, CatalogMaxAgeHours: 24,
		AllocationGB: 15, ReserveGB: 8, EvictionRules: []string{"oldest-completed"}, ProtectIncomplete: true, ProtectLeased: true, PreferredSubtitleLanguage: "ro", FallbackSubtitleLanguage: "en", PreferredAudioLanguage: "en",
		MetadataLanguage: "ro-RO", MetadataFallbackLanguage: "en-US", ArtworkCachePath: "data/artwork", ArtworkCacheMaxBytes: 512 << 20,
		SubDLURL: "https://api.subdl.com", MaxConcurrentJobs: 10, TitleRefreshTimeoutMinutes: 30,
		SubtitleCachePath: "data/subtitles", SubtitleCacheMaxBytes: 256 << 20,
		FFprobePath: "/usr/bin/ffprobe", FFmpegPath: "/usr/bin/ffmpeg",
		WatchedThresholdPercent: 90,
	}
}

type Store struct {
	mu           sync.RWMutex
	path         string
	base         Settings
	value        Settings
	envManaged   map[string]bool
	fileProvided map[string]bool
}

func Load() (*Store, error) {
	path := strings.TrimSpace(os.Getenv(EnvironmentPrefix + "SETTINGS_PATH"))
	if path == "" {
		path = DefaultSettingsPath
	}
	return LoadAt(path)
}

// LoadAt loads the store from an explicit settings file path. The data-dir
// layer calls this after resolving the directory. Relative native paths
// anchor to the settings file's directory (see anchorNativePaths), so the
// effective store never depends on the process working directory.
func LoadAt(path string) (*Store, error) {
	base := Defaults()
	s := &Store{path: path, envManaged: map[string]bool{}, fileProvided: map[string]bool{}}
	b, err := os.ReadFile(s.path)
	if err == nil {
		if err = json.Unmarshal(b, &base); err != nil {
			return nil, fmt.Errorf("decode settings: %w", err)
		}
		if base.InstanceName == "" {
			base.InstanceName = "Torrent TV"
		}
		if base.WatchedThresholdPercent == 0 {
			base.WatchedThresholdPercent = 90
		}
		if base.PreferredAudioLanguage == "" {
			base.PreferredAudioLanguage = "en"
		}
		// Eviction keys postdate older settings files; seed their defaults only
		// when a key is absent so an explicit false or empty list is honored.
		var present map[string]json.RawMessage
		if err := json.Unmarshal(b, &present); err != nil {
			return nil, fmt.Errorf("decode settings: %w", err)
		}
		for _, key := range requiredKeys {
			if _, ok := present[key]; ok {
				s.fileProvided[key] = true
			}
		}
		if _, ok := present["evictionRules"]; !ok {
			base.EvictionRules = []string{"oldest-completed"}
		}
		if _, ok := present["protectIncomplete"]; !ok {
			base.ProtectIncomplete = true
		}
		if _, ok := present["protectLeased"]; !ok {
			base.ProtectLeased = true
		}
		if base.MetadataLanguage == "" {
			base.MetadataLanguage = "ro-RO"
		}
		if base.MetadataFallbackLanguage == "" {
			base.MetadataFallbackLanguage = "en-US"
		}
		if base.ArtworkCachePath == "" {
			base.ArtworkCachePath = "data/artwork"
		}
		if base.ArtworkCacheMaxBytes == 0 {
			base.ArtworkCacheMaxBytes = 512 << 20
		}
		if base.SubDLURL == "" {
			base.SubDLURL = "https://api.subdl.com"
		}
		if base.MaxConcurrentJobs == 0 {
			base.MaxConcurrentJobs = 10
		}
		if base.TitleRefreshTimeoutMinutes == 0 {
			base.TitleRefreshTimeoutMinutes = 30
		}
		if base.FFprobePath == "" {
			base.FFprobePath = "/usr/bin/ffprobe"
		}
		if base.FFmpegPath == "" {
			base.FFmpegPath = "/usr/bin/ffmpeg"
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	effective := base
	managed, err := applyEnvironment(&effective)
	if err != nil {
		return nil, err
	}
	// Relative native paths — defaults and file-provided values alike — are
	// CWD-relative in serve mode, where systemd pins WorkingDirectory next
	// to the data dir. GUI and packaged launches have an arbitrary CWD, so
	// the store pins them to the settings file's directory instead; without
	// this a Finder-launched .app would mkdir "data" under / and die.
	settingsDir, err := filepath.Abs(filepath.Dir(s.path))
	if err != nil {
		return nil, err
	}
	anchorNativePaths(&effective, managed, settingsDir)
	s.base, s.value, s.envManaged = base, effective, managed
	if err := s.validate(effective); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(effective.DatabasePath), 0o750); err != nil {
		return nil, err
	}
	return s, nil
}

// anchorNativePaths pins the settings' filesystem paths to dir (the
// settings file's directory) at load time. Historic defaults are relative
// ("data/...") and resolve against the process CWD in serve mode, where
// WorkingDirectory is the data dir's parent — so a leading "data" element
// is the historic marker for the data dir itself and collapses onto dir.
// That keeps a serve-mode settings file resolving to exactly the same
// absolute paths as before while making CWD-arbitrary launches (GUI .app,
// open, autostart) land inside the data dir. Absolute values and
// environment-managed keys are the operator's explicit word and stay
// untouched, mirroring the supervisor rule this load-time anchoring
// replaces. The file itself is never rewritten: the anchoring recomputes
// deterministically on every load.
func anchorNativePaths(v *Settings, managed map[string]bool, dir string) {
	anchor := func(path string) string {
		if path == "" || filepath.IsAbs(path) {
			return path
		}
		cleaned := filepath.Clean(path)
		if rest, cut := strings.CutPrefix(cleaned, "data"+string(filepath.Separator)); cut {
			cleaned = rest
		} else if cleaned == "data" {
			cleaned = "."
		}
		return filepath.Join(dir, cleaned)
	}
	if !managed["databasePath"] {
		v.DatabasePath = anchor(v.DatabasePath)
	}
	if !managed["downloadRoot"] {
		v.DownloadRoot = anchor(v.DownloadRoot)
	}
	if !managed["torrentSessionDir"] {
		v.TorrentSessionDir = anchor(v.TorrentSessionDir)
	}
	if !managed["artworkCachePath"] {
		v.ArtworkCachePath = anchor(v.ArtworkCachePath)
	}
	if !managed["subtitleCachePath"] {
		v.SubtitleCachePath = anchor(v.SubtitleCachePath)
	}
}
func (s *Store) Get() Settings { s.mu.RLock(); defer s.mu.RUnlock(); return s.value }
func (s *Store) Path() string  { return s.path }
func (s *Store) EnvironmentManaged(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.envManaged[key]
}

func (s *Store) Save(next Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mergeSecrets(&next, s.value)
	persisted := next
	restoreManagedFields(&persisted, s.base, s.envManaged)
	effective := persisted
	managed, err := applyEnvironment(&effective)
	if err != nil {
		return err
	}
	if err := s.validate(effective); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err = os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err = os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err = os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.base, s.value, s.envManaged = persisted, effective, managed
	// The file now carries every persisted key: required keys with a usable
	// value count as file-provided, exactly as if the store had been loaded
	// from this file. Without this, MissingRequired keeps reporting keys the
	// user just saved (the GUI's completing-save auto-start reads it).
	for _, key := range requiredKeys {
		if strings.TrimSpace(requiredValue(persisted, key)) != "" {
			s.fileProvided[key] = true
		}
	}
	return nil
}

func (s *Store) TrustedPrefixes() []netip.Prefix {
	v := s.Get()
	out := make([]netip.Prefix, 0, len(v.TrustedCIDRs))
	for _, raw := range v.TrustedCIDRs {
		if p, e := netip.ParsePrefix(raw); e == nil {
			out = append(out, p)
		}
	}
	return out
}

func (s *Store) PieceWaitTimeout() time.Duration {
	return time.Duration(s.Get().PieceWaitTimeoutSeconds) * time.Second
}

func (s *Store) CatalogMaxAge() time.Duration {
	return time.Duration(s.Get().CatalogMaxAgeHours) * time.Hour
}

func (s *Store) validate(v Settings) error {
	if strings.TrimSpace(v.InstanceName) == "" || v.ListenAddress == "" || v.DatabasePath == "" || v.DownloadRoot == "" {
		return fmt.Errorf("instanceName, listenAddress, databasePath, and downloadRoot are required")
	}
	if v.InitialBufferBytes < 16<<20 || v.InitialBufferBytes > 2<<30 {
		return fmt.Errorf("initialBufferBytes must be between 16 MiB and 2 GiB")
	}
	if v.ReadAheadBytes < v.InitialBufferBytes || v.ReadAheadBytes > 2<<30 {
		return fmt.Errorf("readAheadBytes must be between initialBufferBytes and 2 GiB")
	}
	if v.StreamStartBytes < 256<<10 || v.StreamStartBytes > 64<<20 {
		return fmt.Errorf("streamStartBytes must be between 256 KiB and 64 MiB")
	}
	if v.PieceWaitTimeoutSeconds < 1 {
		return fmt.Errorf("pieceWaitTimeoutSeconds must be positive")
	}
	if err := validateRetentionGB("allocationGb", v.AllocationGB); err != nil {
		return err
	}
	if err := validateRetentionGB("reserveGb", v.ReserveGB); err != nil {
		return err
	}
	if v.WatchedThresholdPercent < 50 || v.WatchedThresholdPercent > 100 {
		return fmt.Errorf("watchedThresholdPercent must be between 50 and 100")
	}
	if v.SubtitleCachePath == "" || v.SubtitleCacheMaxBytes < 1<<20 || v.SubtitleCacheMaxBytes > 4<<30 {
		return fmt.Errorf("subtitleCachePath is required and subtitleCacheMaxBytes must be between 1 MiB and 4 GiB")
	}
	if v.FFprobePath == "" || v.FFmpegPath == "" || !filepath.IsAbs(v.FFprobePath) || !filepath.IsAbs(v.FFmpegPath) {
		return fmt.Errorf("ffprobePath and ffmpegPath must be absolute paths")
	}
	if v.ArtworkCachePath == "" || v.ArtworkCacheMaxBytes < 16<<20 || v.ArtworkCacheMaxBytes > 8<<30 {
		return fmt.Errorf("artworkCachePath is required and artworkCacheMaxBytes must be between 16 MiB and 8 GiB")
	}
	for label, raw := range map[string]string{"SubDL API URL": v.SubDLURL} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("%s must be an absolute URL", label)
		}
	}
	if !strings.EqualFold(strings.TrimSpace(strings.TrimRight(v.SubDLURL, "/")), "https://api.subdl.com") {
		return fmt.Errorf("SubDL API URL must use https://api.subdl.com")
	}
	if v.MaxConcurrentJobs < 1 || v.MaxConcurrentJobs > 20 {
		return fmt.Errorf("maxConcurrentJobs must be between 1 and 20")
	}
	if v.TitleRefreshTimeoutMinutes < 5 || v.TitleRefreshTimeoutMinutes > 120 {
		return fmt.Errorf("titleRefreshTimeoutMinutes must be between 5 and 120")
	}
	for _, raw := range v.TrustedCIDRs {
		if _, err := netip.ParsePrefix(raw); err != nil {
			return fmt.Errorf("invalid trusted CIDR %q", raw)
		}
	}
	switch v.DownloadEngine {
	case "native", "qbittorrent":
	default:
		return fmt.Errorf("downloadEngine must be native or qbittorrent")
	}
	if v.TorrentPeerPort < 0 || v.TorrentPeerPort > 65535 {
		return fmt.Errorf("torrentPeerPort must be between 0 and 65535")
	}
	if strings.TrimSpace(v.TorrentSessionDir) == "" {
		return fmt.Errorf("torrentSessionDir is required")
	}
	for _, rule := range v.EvictionRules {
		if !ValidEvictionRule(rule) {
			return fmt.Errorf("evictionRules contains unknown rule %q; valid rules are %s", strings.TrimSpace(rule), strings.Join(EvictionRuleAtoms, ", "))
		}
	}
	return nil
}

func validateRetentionGB(key string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || (value != 0 && (value < 0.1 || value > 100000)) {
		return fmt.Errorf("%s must be 0 or between 0.1 and 100000 GB", key)
	}
	return nil
}

// EvictionRuleAtoms are the valid evictionRules atoms in canonical spelling.
var EvictionRuleAtoms = []string{"oldest-completed", "newest-completed", "least-recently-played", "most-recently-played", "watched-first", "never-watched-first", "largest", "smallest"}

// ValidEvictionRule reports whether the atom names an eviction ordering rule.
// Matching ignores case and surrounding spaces so the comma-separated browser
// field tolerates sloppy input.
func ValidEvictionRule(rule string) bool {
	return slices.Contains(EvictionRuleAtoms, strings.ToLower(strings.TrimSpace(rule)))
}

// NormalizeEvictionRules trims and lowercases the configured rule list and
// drops blanks; an empty or blank list falls back to the oldest-completed
// default (ADR-0004). Unknown atoms pass through: validation rejects them at
// every boundary that can persist settings.
func NormalizeEvictionRules(rules []string) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		if atom := strings.ToLower(strings.TrimSpace(rule)); atom != "" {
			out = append(out, atom)
		}
	}
	if len(out) == 0 {
		return []string{EvictionRuleAtoms[0]}
	}
	return out
}

func applyEnvironment(settings *Settings) (map[string]bool, error) {
	managed := map[string]bool{}
	value := reflect.ValueOf(settings).Elem()
	typeOf := value.Type()
	for i := 0; i < value.NumField(); i++ {
		fieldType := typeOf.Field(i)
		jsonKey := strings.Split(fieldType.Tag.Get("json"), ",")[0]
		if jsonKey == "" || jsonKey == "-" {
			continue
		}
		environmentKey := EnvironmentPrefix + camelToEnvironment(jsonKey)
		raw, ok := os.LookupEnv(environmentKey)
		if !ok {
			continue
		}
		field := value.Field(i)
		switch field.Kind() {
		case reflect.String:
			field.SetString(raw)
		case reflect.Int, reflect.Int64:
			parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s must be an integer: %w", environmentKey, err)
			}
			field.SetInt(parsed)
		case reflect.Bool:
			parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return nil, fmt.Errorf("%s must be a boolean: %w", environmentKey, err)
			}
			field.SetBool(parsed)
		case reflect.Float32, reflect.Float64:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil {
				return nil, fmt.Errorf("%s must be a number: %w", environmentKey, err)
			}
			field.SetFloat(parsed)
		case reflect.Slice:
			if field.Type().Elem().Kind() != reflect.String {
				return nil, fmt.Errorf("%s uses an unsupported settings type", environmentKey)
			}
			items := []string{}
			for _, item := range strings.Split(raw, ",") {
				if item = strings.TrimSpace(item); item != "" {
					items = append(items, item)
				}
			}
			field.Set(reflect.ValueOf(items))
		default:
			return nil, fmt.Errorf("%s uses an unsupported settings type", environmentKey)
		}
		managed[jsonKey] = true
	}
	return managed, nil
}

func restoreManagedFields(target *Settings, base Settings, managed map[string]bool) {
	targetValue := reflect.ValueOf(target).Elem()
	baseValue := reflect.ValueOf(base)
	typeOf := targetValue.Type()
	for i := 0; i < targetValue.NumField(); i++ {
		jsonKey := strings.Split(typeOf.Field(i).Tag.Get("json"), ",")[0]
		if managed[jsonKey] {
			targetValue.Field(i).Set(baseValue.Field(i))
		}
	}
}

func camelToEnvironment(value string) string {
	var out strings.Builder
	for i, r := range value {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out.WriteByte('_')
		}
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		out.WriteRune(r)
	}
	return out.String()
}

func mergeSecrets(next *Settings, old Settings) {
	if next.FileListPasskey == "" {
		next.FileListPasskey = old.FileListPasskey
	}
	if next.QBittorrentPassword == "" {
		next.QBittorrentPassword = old.QBittorrentPassword
	}
	if next.TMDBAPIKey == "" {
		next.TMDBAPIKey = old.TMDBAPIKey
	}
	if next.SubDLAPIKey == "" {
		next.SubDLAPIKey = old.SubDLAPIKey
	}
	if next.PortalAPIKey == "" {
		next.PortalAPIKey = old.PortalAPIKey
	}
}

// Validate exposes load-time validation so non-HTTP callers (GUI bindings)
// apply exactly the same rules before Save. Save() validates internally;
// this is for pre-checks.
func (s *Store) Validate(v Settings) error { return s.validate(v) }

// RestartRequired reports whether any setting that the running server read
// at startup differs, mirroring the HTTP PUT response contract.
func RestartRequired(old, new Settings) bool {
	return old.ListenAddress != new.ListenAddress || old.DatabasePath != new.DatabasePath ||
		old.MaxConcurrentJobs != new.MaxConcurrentJobs || old.TitleRefreshTimeoutMinutes != new.TitleRefreshTimeoutMinutes ||
		old.DownloadEngine != new.DownloadEngine || old.TorrentPeerPort != new.TorrentPeerPort ||
		old.TorrentSessionDir != new.TorrentSessionDir
}

// EnsureNativePathsWritable prepares the directories the native torrent engine
// writes to at startup: it creates them when missing and proves they accept
// writes. It runs while saving settings so a bad path fails the save with a
// visible error instead of crash-looping the service on the next restart.
// The check runs inside the same process (and therefore the same systemd
// sandbox) as the engine, so it fails exactly when startup would.
func EnsureNativePathsWritable(downloadEngine, downloadRoot, sessionDir string) error {
	if downloadEngine != "native" {
		return nil
	}
	dirs := []struct{ label, path string }{
		{"download root", downloadRoot},
		{"torrent session directory", sessionDir},
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir.path, 0o755); err != nil {
			return fmt.Errorf("cannot create %s %s: %w", dir.label, dir.path, err)
		}
		probe, err := os.CreateTemp(dir.path, ".write-probe-*")
		if err != nil {
			return fmt.Errorf("%s %s is not writable: %w", dir.label, dir.path, err)
		}
		name := probe.Name()
		_ = probe.Close()
		_ = os.Remove(name)
	}
	return nil
}

package updates

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// The fixed repository every automatic update resolves against. Release
// metadata, assets, and checksums are only trusted from these coordinates.
const (
	releaseOwner = "mihaiflorentin88"
	releaseRepo  = "torrent-tv"
	releaseHost  = "github.com"

	// manifestName is the checksum manifest published alongside the release
	// payload. A release without it fails closed.
	manifestName = "SHA256SUMS"
)

// ReleasesURL is the repository release page surfaced in every status.
const ReleasesURL = "https://" + releaseHost + "/" + releaseOwner + "/" + releaseRepo + "/releases"

// Resolution failure classes. Callers classify problems with errors.Is:
// manual-only is a user-visible installation state, the rest are neutral
// upstream or verification problems.
var (
	// ErrManualOnly marks an installation that cannot update itself: a
	// development or invalid build identity, or a platform/flavor without a
	// release asset.
	ErrManualOnly = errors.New("updates: installation is manual-only")

	// ErrNoMatchingRelease marks an announcement that repository metadata
	// does not confirm: wrong tag, unstable release, or missing asset.
	ErrNoMatchingRelease = errors.New("updates: no matching repository release")

	// ErrUntrustedAsset marks release locations outside the fixed
	// repository's HTTPS release infrastructure.
	ErrUntrustedAsset = errors.New("updates: untrusted release location")

	// ErrChecksumManifest marks a missing, malformed, or ambiguous
	// checksum manifest or entry.
	ErrChecksumManifest = errors.New("updates: checksum manifest unusable")
)

// Asset is one downloadable release artifact as published by the repository.
type Asset struct {
	Name string
	URL  string
	// Size is the published byte size of the asset; the downloader uses it
	// to detect and complete truncated transfers when the CDN serves the
	// body without a Content-Length.
	Size int64
}

// Release is the repository metadata for one release.
type Release struct {
	Tag         string
	URL         string
	Notes       string
	Draft       bool
	Prerelease  bool
	PublishedAt time.Time
	Assets      []Asset
}

// MetadataSource is the injectable repository release metadata transport.
// Composition binds it to the repository release feed; tests supply a fake.
// It never receives integration credentials.
type MetadataSource interface {
	// LatestRelease returns the repository's latest published release.
	LatestRelease(context.Context) (Release, error)

	// ChecksumManifest returns the raw checksum manifest text for an
	// already validated manifest asset URL.
	ChecksumManifest(ctx context.Context, manifestURL string) (string, error)
}

// Selection is the exact, verified update target for one installation.
type Selection struct {
	Version     string
	Tag         string
	Notes       string
	ReleasedAt  time.Time
	ReleasesURL string
	AssetName   string
	AssetURL    string
	SHA256      string
	// AssetSize is the published byte size of the selected asset.
	AssetSize int64
}

// Resolver turns the public announcement hint into an exact repository
// selection for one installation identity.
type Resolver struct {
	identity Identity
	source   MetadataSource
}

// NewResolver binds an installation identity to a metadata source.
func NewResolver(identity Identity, source MetadataSource) *Resolver {
	return &Resolver{identity: identity, source: source}
}

// Resolve verifies the announcement hint against repository release metadata
// and returns the exact tag, asset, and checksum to install. The hint never
// selects a URL: every downloaded location is matched against the fixed
// repository's release infrastructure, so a wrong announcement can only
// fail, never redirect. No release bytes are downloaded here.
func (r *Resolver) Resolve(ctx context.Context, hint string) (Selection, error) {
	if !SelfUpdateCapable(r.identity) {
		return Selection{}, fmt.Errorf("%w: build identity %q", ErrManualOnly, r.identity.Version)
	}
	version := normalizeVersion(hint)
	tag := "v" + version
	canonical, ok := canonicalVersion(version)
	if !ok {
		return Selection{}, fmt.Errorf("%w: announced version %q is not a full release version", ErrNoMatchingRelease, hint)
	}
	release, err := r.source.LatestRelease(ctx)
	if err != nil {
		return Selection{}, fmt.Errorf("resolve repository release: %w", err)
	}
	if release.Draft || release.Prerelease {
		return Selection{}, fmt.Errorf("%w: repository release %q is not a stable publication", ErrNoMatchingRelease, release.Tag)
	}
	if release.Tag != tag {
		return Selection{}, fmt.Errorf("%w: repository tag %q does not match announced %q", ErrNoMatchingRelease, release.Tag, tag)
	}
	if semver.Prerelease(canonical) != "" {
		return Selection{}, fmt.Errorf("%w: repository tag %q is not a stable release", ErrNoMatchingRelease, release.Tag)
	}
	releasePath := "/" + releaseOwner + "/" + releaseRepo + "/releases/tag/" + tag
	if err := checkRepositoryURL(release.URL, releasePath); err != nil {
		return Selection{}, fmt.Errorf("release page: %w", err)
	}

	// The tag owns the version text: strip its single prefix and select the
	// exact asset from the release matrix.
	version = strings.TrimPrefix(release.Tag, "v")
	name, ok := assetName(r.identity.Flavor, r.identity.GOOS, r.identity.GOARCH, version)
	if !ok {
		return Selection{}, fmt.Errorf("%w: no release asset for %s/%s %s", ErrManualOnly, r.identity.GOOS, r.identity.GOARCH, r.identity.Flavor)
	}
	asset, found, err := exactAsset(release.Assets, name)
	if err != nil {
		return Selection{}, fmt.Errorf("%w: %s", ErrUntrustedAsset, err)
	}
	if !found {
		return Selection{}, fmt.Errorf("%w: repository release %q publishes no asset %q", ErrNoMatchingRelease, release.Tag, name)
	}
	downloadPath := "/" + releaseOwner + "/" + releaseRepo + "/releases/download/" + tag + "/" + name
	if err := checkRepositoryURL(asset.URL, downloadPath); err != nil {
		return Selection{}, fmt.Errorf("asset %q: %w", name, err)
	}
	manifestAsset, found, err := exactAsset(release.Assets, manifestName)
	if err != nil {
		return Selection{}, fmt.Errorf("%w: %s", ErrChecksumManifest, err)
	}
	if !found {
		return Selection{}, fmt.Errorf("%w: repository release %q publishes no %s manifest", ErrChecksumManifest, release.Tag, manifestName)
	}
	manifestPath := "/" + releaseOwner + "/" + releaseRepo + "/releases/download/" + tag + "/" + manifestName
	if err := checkRepositoryURL(manifestAsset.URL, manifestPath); err != nil {
		return Selection{}, fmt.Errorf("%s manifest: %w", manifestName, err)
	}
	text, err := r.source.ChecksumManifest(ctx, manifestAsset.URL)
	if err != nil {
		return Selection{}, fmt.Errorf("fetch %s: %w", manifestName, err)
	}
	digest, err := manifestDigest(text, name)
	if err != nil {
		return Selection{}, err
	}
	return Selection{
		Version:     version,
		Tag:         release.Tag,
		Notes:       release.Notes,
		ReleasedAt:  release.PublishedAt,
		ReleasesURL: release.URL,
		AssetName:   name,
		AssetURL:    asset.URL,
		SHA256:      digest,
		AssetSize:   asset.Size,
	}, nil
}

// assetName resolves the exact release asset for an installation flavor from
// the release matrix. Asset names follow the release naming standard
// torrent-tv-<version>-<platform>[-<flavor>].<ext>, where the flavor marker
// tells the deployment shape: desktop (GUI app + server), headless (server
// only), cli (terminal binary that can open the desktop UI or serve), and
// app (the packaged macOS .app). ok is false when the combination has no
// release asset, which makes the installation manual-only.
func assetName(flavor, goos, goarch, version string) (string, bool) {
	base := "torrent-tv-" + version + "-"
	switch goos {
	case "linux":
		switch flavor {
		case FlavorGUI:
			switch goarch {
			case "amd64", "arm64":
				return base + "linux-" + goarch + "-desktop.tar.gz", true
			}
		case FlavorHeadless:
			switch goarch {
			case "amd64", "arm64":
				return base + "linux-" + goarch + "-headless.tar.gz", true
			case "arm":
				return base + "linux-armv7-headless.tar.gz", true
			}
		}
	case "windows":
		if flavor == FlavorGUI {
			switch goarch {
			case "amd64", "arm64":
				return base + "windows-" + goarch + "-desktop.zip", true
			}
		}
	case "darwin":
		switch flavor {
		case FlavorGUI:
			switch goarch {
			case "amd64", "arm64":
				return base + "macos-" + goarch + "-cli.tar.gz", true
			}
		case FlavorBundle:
			return base + "macos-universal-app.zip", true
		}
	}
	return "", false
}

// exactAsset returns the single asset named name. Published asset names are
// unique: zero matches are reported as absent, several fail closed.
func exactAsset(assets []Asset, name string) (Asset, bool, error) {
	var found Asset
	matches := 0
	for _, asset := range assets {
		if asset.Name != name {
			continue
		}
		found = asset
		matches++
	}
	switch matches {
	case 1:
		return found, true, nil
	case 0:
		return Asset{}, false, nil
	default:
		return Asset{}, false, fmt.Errorf("duplicate %q asset", name)
	}
}

// checkRepositoryURL reports whether rawurl is an HTTPS repository release
// location at exactly wantPath without embedded credentials. Anything else
// is untrusted.
func checkRepositoryURL(rawurl, wantPath string) error {
	parsed, err := url.Parse(rawurl)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrUntrustedAsset, err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("%w: release location must use HTTPS", ErrUntrustedAsset)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: release location must not carry credentials", ErrUntrustedAsset)
	}
	if parsed.Host != releaseHost {
		return fmt.Errorf("%w: release host %q is not %q", ErrUntrustedAsset, parsed.Host, releaseHost)
	}
	if parsed.Path != wantPath {
		return fmt.Errorf("%w: release path %q is outside the fixed repository", ErrUntrustedAsset, parsed.Path)
	}
	return nil
}

// manifestDigest returns the manifest checksum for name. The manifest must
// parse cleanly: every non-empty line is "<checksum><whitespace><filename>",
// filenames are unique, and the requested entry must exist. A release
// without a usable manifest never resolves.
func manifestDigest(text, name string) (string, error) {
	digest := ""
	seen := make(map[string]int)
	for number, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return "", fmt.Errorf("%w: manifest line %d is malformed", ErrChecksumManifest, number+1)
		}
		checksum := strings.ToLower(fields[0])
		if !hexDigest(checksum) {
			return "", fmt.Errorf("%w: manifest line %d carries a checksum that is not 64 hexadecimal digits", ErrChecksumManifest, number+1)
		}
		filename := strings.TrimPrefix(fields[1], "*")
		if previous, duplicate := seen[filename]; duplicate {
			return "", fmt.Errorf("%w: manifest line %d duplicates %q from line %d", ErrChecksumManifest, number+1, filename, previous)
		}
		seen[filename] = number + 1
		if filename == name {
			digest = checksum
		}
	}
	if digest == "" {
		return "", fmt.Errorf("%w: manifest has no entry for %q", ErrChecksumManifest, name)
	}
	return digest, nil
}

// hexDigest reports whether text is exactly 64 hexadecimal digits.
func hexDigest(text string) bool {
	if len(text) != 64 {
		return false
	}
	for _, digit := range text {
		switch {
		case digit >= '0' && digit <= '9', digit >= 'a' && digit <= 'f', digit >= 'A' && digit <= 'F':
		default:
			return false
		}
	}
	return true
}

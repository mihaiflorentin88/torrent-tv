package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

const testVersion = "0.4.0"

var testPublishedAt = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// digestOf derives a real 64-hex checksum for a fixture asset name.
func digestOf(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

// matrixAsset returns the exact release asset name for one row of the
// release matrix, mirroring the published archive naming.
func matrixAsset(t *testing.T, version, goos, goarch, flavor string) string {
	t.Helper()
	name, ok := assetName(flavor, goos, goarch, version)
	if !ok {
		t.Fatalf("matrix row %s/%s %s has no release asset", goos, goarch, flavor)
	}
	return name
}

// stableRelease builds a complete repository release for version: every
// matrix asset plus the checksum manifest, all under the fixed repository.
func stableRelease(version string) Release {
	tag := "v" + version
	names := []string{
		fmt.Sprintf("torrent-tv-%s-linux-amd64.tar.gz", version),
		fmt.Sprintf("torrent-tv-%s-linux-arm64.tar.gz", version),
		fmt.Sprintf("torrent-tv-%s-linux-amd64-headless.tar.gz", version),
		fmt.Sprintf("torrent-tv-%s-linux-arm64-headless.tar.gz", version),
		fmt.Sprintf("torrent-tv-%s-linux-armv7.tar.gz", version),
		fmt.Sprintf("torrent-tv-%s-windows-amd64.zip", version),
		fmt.Sprintf("torrent-tv-%s-windows-arm64.zip", version),
		fmt.Sprintf("torrent-tv-%s-darwin-amd64.tar.gz", version),
		fmt.Sprintf("torrent-tv-%s-darwin-arm64.tar.gz", version),
		fmt.Sprintf("torrent-tv-%s-darwin-universal.zip", version),
	}
	release := Release{Tag: tag, Notes: "release notes", Draft: false, Prerelease: false, PublishedAt: testPublishedAt}
	release.URL = fmt.Sprintf("https://github.com/%s/%s/releases/tag/%s", releaseOwner, releaseRepo, tag)
	for _, name := range names {
		release.Assets = append(release.Assets, Asset{
			Name: name,
			URL:  fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s", releaseOwner, releaseRepo, tag, name),
		})
	}
	release.Assets = append(release.Assets, Asset{
		Name: "SHA256SUMS",
		URL:  fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/SHA256SUMS", releaseOwner, releaseRepo, tag),
	})
	return release
}

// manifestText renders the sha256sum manifest covering exactly assets.
func manifestText(assets []Asset) string {
	var lines []string
	for _, asset := range assets {
		if asset.Name == "SHA256SUMS" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  %s", digestOf(asset.Name), asset.Name))
	}
	return strings.Join(lines, "\n") + "\n"
}

// testIdentity returns the linux amd64 GUI installation identity.
func testIdentity() Identity {
	return Identity{Version: "0.3.0", GOOS: "linux", GOARCH: "amd64", Flavor: FlavorGUI}
}

// fakeSource is an in-memory stand-in for the repository metadata transport.
type fakeSource struct {
	mu          sync.Mutex
	release     Release
	manifest    string
	releaseErr  error
	manifestErr error

	manifestRequests []string
}

func (f *fakeSource) LatestRelease(context.Context) (Release, error) {
	if f.releaseErr != nil {
		return Release{}, f.releaseErr
	}
	return f.release, nil
}

func (f *fakeSource) ChecksumManifest(_ context.Context, url string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifestRequests = append(f.manifestRequests, url)
	if f.manifestErr != nil {
		return "", f.manifestErr
	}
	return f.manifest, nil
}

func (f *fakeSource) manifestRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.manifestRequests)
}

func TestVersionNormalizeStripsSingleLeadingV(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare version", "0.4.0", "0.4.0"},
		{"single tag prefix", "v0.4.0", "0.4.0"},
		{"surrounding space", "  v0.4.0 ", "0.4.0"},
		{"only one prefix stripped", "vv0.4.0", "v0.4.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeVersion(tt.input); got != tt.want {
				t.Fatalf("normalizeVersion(%q) = %q, want %q", tt.input, got, tt.want)
			}
			canonical, ok := canonicalVersion(normalizeVersion(tt.input))
			if wantValid := tt.input != "vv0.4.0"; ok != wantValid {
				t.Fatalf("canonicalVersion(%q) valid = %v, want %v", normalizeVersion(tt.input), ok, wantValid)
			}
			if ok && canonical != "v"+tt.want {
				t.Fatalf("canonicalVersion = %q, want %q", canonical, "v"+tt.want)
			}
		})
	}
}

func TestVersionIsNewerHonorsCurrentOlderAndPrereleasePrecedence(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		candidate string
		want      bool
		wantErr   bool
	}{
		{"current release is a no-op", "0.4.0", "0.4.0", false, false},
		{"older release", "0.4.0", "0.3.9", false, false},
		{"newer release", "0.3.0", "0.4.0", true, false},
		{"prerelease sorts before its stable", "0.4.0", "0.4.0-rc.1", false, false},
		{"stable sorts after its prerelease", "0.4.0-rc.1", "0.4.0", true, false},
		{"prerelease precedence within series", "0.4.0-rc.1", "0.4.0-rc.2", true, false},
		{"malformed current", "0.4", "0.5.0", false, true},
		{"malformed candidate", "0.4.0", "next", false, true},
		{"dev current", "dev", "0.5.0", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IsNewer(tt.current, tt.candidate)
			if (err != nil) != tt.wantErr {
				t.Fatalf("IsNewer(%q, %q) error = %v, wantErr %v", tt.current, tt.candidate, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.candidate, got, tt.want)
			}
		})
	}
}

func TestVersionIdentityValidityGatesSelfUpdate(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"0.3.0", true},
		{"v0.3.0", true},
		{"0.3.0-rc.1", true},
		{"dev", false},
		{"", false},
		{"0.3", false},
		{"0.3.0.1", false},
		{"release", false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			identity := Identity{Version: tt.version, GOOS: "linux", GOARCH: "amd64", Flavor: FlavorGUI}
			if got := SelfUpdateCapable(identity); got != tt.want {
				t.Fatalf("SelfUpdateCapable(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestReleaseResolutionSelectsExactRepositoryAssets(t *testing.T) {
	release := stableRelease(testVersion)
	manifest := manifestText(release.Assets)
	identity := testIdentity()

	source := &fakeSource{release: release, manifest: manifest}
	resolver := NewResolver(identity, source)
	selection, err := resolver.Resolve(context.Background(), testVersion)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	assetName := matrixAsset(t, testVersion, "linux", "amd64", FlavorGUI)
	wantURL := fmt.Sprintf("https://github.com/%s/%s/releases/download/v%s/%s", releaseOwner, releaseRepo, testVersion, assetName)
	if selection.Version != testVersion || selection.Tag != "v"+testVersion {
		t.Fatalf("selection version/tag = %q/%q, want %q/v%s", selection.Version, selection.Tag, testVersion, testVersion)
	}
	if selection.AssetName != assetName {
		t.Fatalf("selected asset %q, want %q", selection.AssetName, assetName)
	}
	if selection.AssetURL != wantURL {
		t.Fatalf("selected asset URL %q, want %q", selection.AssetURL, wantURL)
	}
	if selection.SHA256 != digestOf(assetName) {
		t.Fatalf("selection checksum %q, want manifest digest %q", selection.SHA256, digestOf(assetName))
	}
	if selection.ReleasesURL != release.URL || selection.Notes != release.Notes || !selection.ReleasedAt.Equal(testPublishedAt) {
		t.Fatalf("selection metadata = %+v, want release metadata %+v", selection, release)
	}
	if want := fmt.Sprintf("https://github.com/%s/%s/releases/download/v%s/SHA256SUMS", releaseOwner, releaseRepo, testVersion); source.manifestRequestCount() != 1 || source.manifestRequests[0] != want {
		t.Fatalf("manifest requested %v, want exactly [%q]", source.manifestRequests, want)
	}

	// The whole release matrix resolves to its exact archive.
	matrix := []struct{ goos, goarch, flavor string }{
		{"linux", "amd64", FlavorGUI},
		{"linux", "arm64", FlavorGUI},
		{"linux", "amd64", FlavorHeadless},
		{"linux", "arm64", FlavorHeadless},
		{"linux", "arm", FlavorHeadless},
		{"windows", "amd64", FlavorGUI},
		{"windows", "arm64", FlavorGUI},
		{"darwin", "amd64", FlavorGUI},
		{"darwin", "arm64", FlavorGUI},
		{"darwin", "arm64", FlavorBundle},
	}
	for _, row := range matrix {
		t.Run(row.goos+"-"+row.goarch+"-"+row.flavor, func(t *testing.T) {
			resolver := NewResolver(Identity{Version: "0.3.0", GOOS: row.goos, GOARCH: row.goarch, Flavor: row.flavor}, source)
			selection, err := resolver.Resolve(context.Background(), testVersion)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			wantAsset := matrixAsset(t, testVersion, row.goos, row.goarch, row.flavor)
			if selection.AssetName != wantAsset {
				t.Fatalf("selected asset %q, want %q", selection.AssetName, wantAsset)
			}
			if selection.SHA256 != digestOf(wantAsset) {
				t.Fatalf("selection checksum %q, want %q", selection.SHA256, digestOf(wantAsset))
			}
		})
	}
}

func TestReleaseResolutionRejectsDraftPrereleaseAndUnstableTag(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Release)
		version string
	}{
		{"draft release", func(r *Release) { r.Draft = true }, testVersion},
		{"prerelease flag", func(r *Release) { r.Prerelease = true }, testVersion},
		{"prerelease tag without flag", func(r *Release) { r.Tag = "v0.4.0-rc.1" }, "0.4.0-rc.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release := stableRelease(tt.version)
			tt.mutate(&release)
			source := &fakeSource{release: release, manifest: manifestText(release.Assets)}
			resolver := NewResolver(testIdentity(), source)
			if _, err := resolver.Resolve(context.Background(), tt.version); !errors.Is(err, ErrNoMatchingRelease) {
				t.Fatalf("Resolve() error = %v, want ErrNoMatchingRelease", err)
			}
		})
	}
}

func TestReleaseResolutionRejectsWrongTag(t *testing.T) {
	release := stableRelease("0.3.0")
	source := &fakeSource{release: release, manifest: manifestText(release.Assets)}
	resolver := NewResolver(testIdentity(), source)
	_, err := resolver.Resolve(context.Background(), testVersion)
	if !errors.Is(err, ErrNoMatchingRelease) {
		t.Fatalf("Resolve() error = %v, want ErrNoMatchingRelease", err)
	}
	if source.manifestRequestCount() != 0 {
		t.Fatalf("manifest fetched %d times for a mismatched tag, want 0", source.manifestRequestCount())
	}
}

func TestReleaseResolutionRejectsWrongRepository(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Release)
	}{
		{
			"release page from another repository",
			func(r *Release) { r.URL = "https://github.com/attacker/mirror/releases/tag/v" + testVersion },
		},
		{
			"download URL from another repository",
			func(r *Release) {
				assets := r.Assets
				for i, asset := range assets {
					if strings.Contains(asset.Name, "linux-amd64.tar.gz") && !strings.Contains(asset.Name, "headless") {
						assets[i].URL = "https://github.com/attacker/mirror/releases/download/v" + testVersion + "/" + asset.Name
					}
				}
			},
		},
		{
			"insecure transport",
			func(r *Release) {
				for i, asset := range r.Assets {
					if asset.Name == "SHA256SUMS" {
						r.Assets[i].URL = "http://github.com/mihaiflorentin88/torrent-tv/releases/download/v" + testVersion + "/SHA256SUMS"
					}
				}
			},
		},
		{
			"embedded credentials",
			func(r *Release) {
				for i, asset := range r.Assets {
					if asset.Name == "SHA256SUMS" {
						r.Assets[i].URL = "https://user:secret@github.com/mihaiflorentin88/torrent-tv/releases/download/v" + testVersion + "/SHA256SUMS"
					}
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release := stableRelease(testVersion)
			tt.mutate(&release)
			source := &fakeSource{release: release, manifest: manifestText(release.Assets)}
			resolver := NewResolver(testIdentity(), source)
			if _, err := resolver.Resolve(context.Background(), testVersion); !errors.Is(err, ErrUntrustedAsset) {
				t.Fatalf("Resolve() error = %v, want ErrUntrustedAsset", err)
			}
		})
	}
}

func TestReleaseChecksumManifestIsMandatory(t *testing.T) {
	t.Run("missing manifest asset", func(t *testing.T) {
		release := stableRelease(testVersion)
		release.Assets = release.Assets[:len(release.Assets)-1]
		source := &fakeSource{release: release, manifest: manifestText(release.Assets)}
		resolver := NewResolver(testIdentity(), source)
		if _, err := resolver.Resolve(context.Background(), testVersion); !errors.Is(err, ErrChecksumManifest) {
			t.Fatalf("Resolve() error = %v, want ErrChecksumManifest", err)
		}
	})

	t.Run("missing checksum entry", func(t *testing.T) {
		release := stableRelease(testVersion)
		selected := matrixAsset(t, testVersion, "linux", "amd64", FlavorGUI)
		source := &fakeSource{release: release, manifest: manifestText(release.Assets) + ""}
		// Manifest without the selected asset's entry.
		var lines []string
		for _, asset := range release.Assets {
			if asset.Name == "SHA256SUMS" || asset.Name == selected {
				continue
			}
			lines = append(lines, digestOf(asset.Name)+"  "+asset.Name)
		}
		source.manifest = strings.Join(lines, "\n") + "\n"
		resolver := NewResolver(testIdentity(), source)
		if _, err := resolver.Resolve(context.Background(), testVersion); !errors.Is(err, ErrChecksumManifest) {
			t.Fatalf("Resolve() error = %v, want ErrChecksumManifest", err)
		}
	})

	t.Run("digest must be 64 hexadecimal digits", func(t *testing.T) {
		for name, digest := range map[string]string{
			"too short": strings.Repeat("a", 63),
			"too long":  strings.Repeat("a", 65),
			"not hex":   strings.Repeat("g", 64),
			"empty":     "",
		} {
			t.Run(name, func(t *testing.T) {
				release := stableRelease(testVersion)
				selected := matrixAsset(t, testVersion, "linux", "amd64", FlavorGUI)
				manifest := fmt.Sprintf("%s  %s\n", digest, selected)
				source := &fakeSource{release: release, manifest: manifest}
				resolver := NewResolver(testIdentity(), source)
				if _, err := resolver.Resolve(context.Background(), testVersion); !errors.Is(err, ErrChecksumManifest) {
					t.Fatalf("Resolve() error = %v, want ErrChecksumManifest", err)
				}
			})
		}
	})

	t.Run("malformed manifest line", func(t *testing.T) {
		release := stableRelease(testVersion)
		selected := matrixAsset(t, testVersion, "linux", "amd64", FlavorGUI)
		manifest := fmt.Sprintf("%s  %s\nnot-a-checksum-line\n", digestOf(selected), selected)
		source := &fakeSource{release: release, manifest: manifest}
		resolver := NewResolver(testIdentity(), source)
		if _, err := resolver.Resolve(context.Background(), testVersion); !errors.Is(err, ErrChecksumManifest) {
			t.Fatalf("Resolve() error = %v, want ErrChecksumManifest", err)
		}
	})

	t.Run("manifest fetch failure", func(t *testing.T) {
		release := stableRelease(testVersion)
		source := &fakeSource{release: release, manifestErr: errors.New("metadata transport failed")}
		resolver := NewResolver(testIdentity(), source)
		if _, err := resolver.Resolve(context.Background(), testVersion); err == nil || errors.Is(err, ErrChecksumManifest) {
			t.Fatalf("Resolve() error = %v, want wrapped transport failure", err)
		}
	})
}

func TestReleaseChecksumRejectsDuplicateEntry(t *testing.T) {
	release := stableRelease(testVersion)
	selected := matrixAsset(t, testVersion, "linux", "amd64", FlavorGUI)
	manifest := fmt.Sprintf("%s  %s\n%s  %s\n", digestOf(selected), selected, digestOf("other"), selected)
	source := &fakeSource{release: release, manifest: manifest}
	resolver := NewResolver(testIdentity(), source)
	if _, err := resolver.Resolve(context.Background(), testVersion); !errors.Is(err, ErrChecksumManifest) {
		t.Fatalf("Resolve() error = %v, want ErrChecksumManifest", err)
	}
}

func TestReleaseResolutionFlavorMismatchNeverSelectsWrongArchive(t *testing.T) {
	// Only the GUI archive is published; a headless installation must fail
	// closed instead of taking the GUI archive.
	guiOnly := stableRelease(testVersion)
	headlessName := matrixAsset(t, testVersion, "linux", "arm64", FlavorHeadless)
	var kept []Asset
	for _, asset := range guiOnly.Assets {
		if asset.Name == headlessName {
			continue
		}
		kept = append(kept, asset)
	}
	guiOnly.Assets = kept
	source := &fakeSource{release: guiOnly, manifest: manifestText(guiOnly.Assets)}
	resolver := NewResolver(Identity{Version: "0.3.0", GOOS: "linux", GOARCH: "arm64", Flavor: FlavorHeadless}, source)
	selection, err := resolver.Resolve(context.Background(), testVersion)
	if !errors.Is(err, ErrNoMatchingRelease) {
		t.Fatalf("Resolve() error = %v, want ErrNoMatchingRelease", err)
	}
	if selection.AssetName != "" {
		t.Fatalf("selected asset %q despite flavor mismatch, want no selection", selection.AssetName)
	}
}

func TestReleaseResolutionMalformedAnnouncementIsNeutral(t *testing.T) {
	release := stableRelease(testVersion)
	source := &fakeSource{release: release, manifest: manifestText(release.Assets)}
	for _, hint := range []string{"0.4", "", "not-a-version", "vv0.4.0", "0.4.0.0"} {
		resolver := NewResolver(testIdentity(), source)
		if _, err := resolver.Resolve(context.Background(), hint); !errors.Is(err, ErrNoMatchingRelease) {
			t.Fatalf("Resolve(%q) error = %v, want ErrNoMatchingRelease", hint, err)
		}
	}
}

func TestReleaseResolutionDevIdentityIsManualOnly(t *testing.T) {
	release := stableRelease(testVersion)
	source := &fakeSource{release: release, manifest: manifestText(release.Assets)}

	resolver := NewResolver(Identity{Version: "dev", GOOS: "linux", GOARCH: "amd64", Flavor: FlavorGUI}, source)
	if _, err := resolver.Resolve(context.Background(), testVersion); !errors.Is(err, ErrManualOnly) {
		t.Fatalf("Resolve() error = %v, want ErrManualOnly", err)
	}

	// A combination without a release-asset row is manual-only too.
	resolver = NewResolver(Identity{Version: "0.3.0", GOOS: "linux", GOARCH: "386", Flavor: FlavorGUI}, source)
	if _, err := resolver.Resolve(context.Background(), testVersion); !errors.Is(err, ErrManualOnly) {
		t.Fatalf("Resolve() error = %v, want ErrManualOnly", err)
	}
}

func TestReleasePageConstantMatchesRepository(t *testing.T) {
	if want := "https://github.com/mihaiflorentin88/torrent-tv/releases"; ReleasesURL != want {
		t.Fatalf("ReleasesURL = %q, want %q", ReleasesURL, want)
	}
}

func TestVersionDetectFlavorForcesHeadlessFallback(t *testing.T) {
	tests := []struct {
		name          string
		goos, goarch  string
		bundleInstall bool
		want          string
	}{
		{"linux ARM is headless without the build tag", "linux", "arm", false, FlavorHeadless},
		{"linux ARM bundle shape wins over the headless fallback", "linux", "arm", true, FlavorBundle},
		{"macOS .app install shape is bundle", "darwin", "arm64", true, FlavorBundle},
		{"macOS raw install follows the build tag", "darwin", "arm64", false, buildFlavor},
		{"windows install follows the build tag", "windows", "amd64", false, buildFlavor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectFlavor(tt.goos, tt.goarch, tt.bundleInstall); got != tt.want {
				t.Fatalf("DetectFlavor(%q, %q, %v) = %q, want %q", tt.goos, tt.goarch, tt.bundleInstall, got, tt.want)
			}
		})
	}
	switch buildFlavor {
	case FlavorGUI, FlavorHeadless:
	default:
		t.Fatalf("build-tag flavor constant %q is not a known flavor", buildFlavor)
	}
}

package updates

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// digestHex returns the manifest-form checksum of data.
func digestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// testSelection builds a selection for an asset with the given content.
func testSelection(assetName, version string, content []byte) Selection {
	return Selection{
		Version:   version,
		Tag:       "v" + version,
		AssetName: assetName,
		AssetURL:  ReleasesURL + "/download/v" + version + "/" + assetName,
		SHA256:    digestHex(content),
	}
}

// fixtureBinaries caches tiny fixture executables built from
// testdata/updatefixture for explicit platform and identity combinations.
var (
	fixtureMu     sync.Mutex
	fixtureCache  = map[string]string{}
	fixtureSrcDir = "internal/application/updates/testdata/updatefixture"
)

// fixtureBinary builds and returns the path of a fixture executable for the
// requested platform. version empty omits the injected -ldflags version,
// mirroring the -trimpath release builds that do not record it.
func fixtureBinary(t *testing.T, goos, goarch string, goarm int, tags []string, version string) string {
	t.Helper()
	key := fmt.Sprintf("%s/%s/%d/%s/%s", goos, goarch, goarm, strings.Join(tags, ","), version)
	fixtureMu.Lock()
	defer fixtureMu.Unlock()
	if path, ok := fixtureCache[key]; ok {
		return path
	}
	if dir := fixtureCache["__dir"]; dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "filelist-update-fixtures-")
		if err != nil {
			t.Fatalf("fixture cache dir: %v", err)
		}
		fixtureCache["__dir"] = dir
	}
	out := filepath.Join(fixtureCache["__dir"], fmt.Sprintf("fixture-%s-%s-%s%s-%s", goos, goarch, strings.Join(tags, "-"), map[bool]string{true: "-arm" + fmt.Sprint(goarm), false: ""}[goarm > 0], strings.ReplaceAll(version, ".", "-")))
	root := moduleRoot(t)
	args := []string{"build", "-o", out}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	if version != "" {
		args = append(args, "-ldflags", "-X "+compositionPath+".Version="+version)
	}
	args = append(args, "./"+fixtureSrcDir)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	if goarm > 0 {
		cmd.Env = append(cmd.Env, fmt.Sprintf("GOARM=%d", goarm))
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture %s: %v\n%s", key, err, output)
	}
	fixtureCache[key] = out
	return out
}

// moduleRoot walks up from the package directory to the module root.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("module root not found")
		}
		dir = parent
	}
}

// fileBytes reads a fixture binary's content.
func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// tarMember describes one tar archive member.
type tarMember struct {
	name string
	typ  byte
	data []byte
	link string
	mode int64
}

// buildTarGz renders a .tar.gz with the release layout anchor (./ prefix).
func buildTarGz(t *testing.T, members []tarMember) []byte {
	t.Helper()
	var tarBuffer bytes.Buffer
	writer := tar.NewWriter(&tarBuffer)
	for _, member := range members {
		header := &tar.Header{Name: member.name, Mode: member.mode}
		if member.typ == 0 {
			member.typ = tar.TypeReg
		}
		header.Typeflag = member.typ
		switch member.typ {
		case tar.TypeReg:
			header.Size = int64(len(member.data))
		case tar.TypeSymlink:
			header.Linkname = member.link
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("tar header %q: %v", member.name, err)
		}
		if member.typ == tar.TypeReg && len(member.data) > 0 {
			if _, err := writer.Write(member.data); err != nil {
				t.Fatalf("tar data %q: %v", member.name, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	var gzBuffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&gzBuffer)
	if _, err := gzipWriter.Write(tarBuffer.Bytes()); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return gzBuffer.Bytes()
}

// zipMember describes one zip archive member.
type zipMember struct {
	name string
	dir  bool
	data []byte
	link string // non-empty creates a symlink entry with this target
}

// buildZip renders a .zip matching the published archive shapes.
func buildZip(t *testing.T, members []zipMember) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, member := range members {
		name := member.name
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		switch {
		case member.link != "":
			header.SetMode(os.ModeSymlink | 0o777)
		case member.dir:
			header.Name += "/"
			header.SetMode(os.ModeDir | 0o755)
		default:
			header.SetMode(0o755)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("zip header %q: %v", name, err)
		}
		content := member.data
		if member.link != "" {
			content = []byte(member.link)
		}
		if len(content) > 0 {
			if _, err := entry.Write(content); err != nil {
				t.Fatalf("zip data %q: %v", name, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buffer.Bytes()
}

// stageData stages content into dest and returns the verified staged file.
func stageData(t *testing.T, dest string, content []byte, sel Selection, limits Limits) *Staged {
	t.Helper()
	staged, err := StageArchive(dest, sel, bytes.NewReader(content), limits)
	if err != nil {
		t.Fatalf("StageArchive: %v", err)
	}
	return staged
}

func defaultTestLimits() Limits {
	return Limits{Compressed: 8 << 20, Expanded: 16 << 20, Entries: 64}
}

func tarAssetName(version, goarch string) string {
	return "torrent-tv-" + version + "-linux-" + goarch + ".tar.gz"
}

// happyTarMembers mirrors the published tar layout: a ./ anchored payload
// executable plus a README.
func happyTarMembers(payload []byte) []tarMember {
	return []tarMember{
		{name: ".", typ: tar.TypeDir, mode: 0o755},
		{name: "./torrent-tv", data: payload, mode: 0o755},
		{name: "./README.md", data: []byte("filelist streaming\n"), mode: 0o644},
	}
}

func TestStageStreamsHashesAndVerifiesChecksum(t *testing.T) {
	dest := t.TempDir()
	payload := fileBytes(t, fixtureBinary(t, "linux", "amd64", 0, nil, "0.4.0"))
	archive := buildTarGz(t, happyTarMembers(payload))
	sel := testSelection(tarAssetName("0.4.0", "amd64"), "0.4.0", archive)

	staged := stageData(t, dest, archive, sel, defaultTestLimits())

	info, err := os.Stat(staged.Path)
	if err != nil {
		t.Fatalf("stat staged file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("staged file mode = %v, want 0600", got)
	}
	if hex.EncodeToString(staged.Digest[:]) != sel.SHA256 {
		t.Errorf("staged digest %x does not match selection", staged.Digest)
	}
	if filepath.Dir(staged.Path) != dest {
		t.Errorf("staged file %s must live on the destination filesystem %s", staged.Path, dest)
	}

	target := Identity{Version: "0.3.0", GOOS: "linux", GOARCH: "amd64", Flavor: FlavorGUI}.Target()
	payloadResult, err := staged.Extract(dest, target, defaultTestLimits())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if payloadResult.Kind != PayloadFile {
		t.Errorf("payload kind = %v, want file", payloadResult.Kind)
	}
	exeInfo, err := os.Stat(payloadResult.Executable)
	if err != nil {
		t.Fatalf("stat extracted executable: %v", err)
	}
	if got := exeInfo.Mode().Perm(); got != 0o755 {
		t.Errorf("extracted executable mode = %v, want 0755", got)
	}
	if !bytes.Equal(fileBytes(t, payloadResult.Executable), payload) {
		t.Error("extracted executable content differs from fixture payload")
	}
}

func TestStageRejectsCorruptDownloadAndLeavesNoResidue(t *testing.T) {
	dest := t.TempDir()
	payload := fileBytes(t, fixtureBinary(t, "linux", "amd64", 0, nil, "0.4.0"))
	archive := buildTarGz(t, happyTarMembers(payload))
	sel := testSelection(tarAssetName("0.4.0", "amd64"), "0.4.0", archive)

	corrupt := append([]byte(nil), archive...)
	corrupt[len(corrupt)/2] ^= 0xff
	_, err := StageArchive(dest, sel, bytes.NewReader(corrupt), defaultTestLimits())
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("StageArchive error = %v, want ErrChecksumMismatch", err)
	}
	entries, readErr := os.ReadDir(dest)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".filelist-") {
			t.Errorf("staging residue left behind: %s", entry.Name())
		}
	}
}

func TestStageRejectsOversizedStream(t *testing.T) {
	dest := t.TempDir()
	sel := testSelection("torrent-tv-0.4.0-linux-amd64-desktop.tar.gz", "0.4.0", []byte("payload"))
	limits := Limits{Compressed: 16, Expanded: 1 << 20, Entries: 10}
	_, err := StageArchive(dest, sel, bytes.NewReader([]byte(strings.Repeat("x", 64))), limits)
	if !errors.Is(err, ErrStagingLimit) {
		t.Fatalf("StageArchive error = %v, want ErrStagingLimit", err)
	}
	entries, _ := os.ReadDir(dest)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".filelist-") {
			t.Errorf("staging residue left behind: %s", entry.Name())
		}
	}
}

func TestStageImposesNoMinimumSize(t *testing.T) {
	dest := t.TempDir()
	sel := testSelection("torrent-tv-0.4.0-linux-amd64-desktop.tar.gz", "0.4.0", nil)
	staged, err := StageArchive(dest, sel, bytes.NewReader(nil), defaultTestLimits())
	if err != nil {
		t.Fatalf("StageArchive of empty stream: %v", err)
	}
	info, err := os.Stat(staged.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Errorf("staged size = %d, want 0", info.Size())
	}
}

func TestExtractRejectsTruncatedArchive(t *testing.T) {
	dest := t.TempDir()
	payload := fileBytes(t, fixtureBinary(t, "linux", "amd64", 0, nil, "0.4.0"))
	archive := buildTarGz(t, happyTarMembers(payload))
	truncated := archive[:len(archive)-9]
	sel := testSelection(tarAssetName("0.4.0", "amd64"), "0.4.0", truncated)

	staged := stageData(t, dest, truncated, sel, defaultTestLimits())
	_, err := staged.Extract(dest, Identity{GOOS: "linux", GOARCH: "amd64", Flavor: FlavorGUI}.Target(), defaultTestLimits())
	if !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("Extract error = %v, want ErrArchiveInvalid", err)
	}
}

func TestExtractRejectsTraversalAbsoluteAndDeviceMembers(t *testing.T) {
	payload := fileBytes(t, fixtureBinary(t, "linux", "amd64", 0, nil, "0.4.0"))
	cases := []struct {
		name    string
		members []tarMember
		wantErr error
	}{
		{
			name: "traversal",
			members: append(happyTarMembers(payload), tarMember{
				name: "./../escaped", data: []byte("nope"),
			}),
		},
		{
			name: "absolute",
			members: append(happyTarMembers(payload), tarMember{
				name: "/etc/filelist-escaped", data: []byte("nope"),
			}),
		},
		{
			name: "nested traversal",
			members: append(happyTarMembers(payload), tarMember{
				name: "./docs/../../escaped", data: []byte("nope"),
			}),
		},
		{
			name: "device node",
			members: append(happyTarMembers(payload), tarMember{
				name: "./dev/zero", typ: tar.TypeChar,
			}),
		},
		{
			name: "duplicate member",
			members: append(happyTarMembers(payload), tarMember{
				name: "./README.md", data: []byte("again"),
			}),
		},
		{
			name: "file flavor symlink",
			members: append(happyTarMembers(payload), tarMember{
				name: "./torrent-tv-link", typ: tar.TypeSymlink, link: "torrent-tv",
			}),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dest := t.TempDir()
			archive := buildTarGz(t, testCase.members)
			sel := testSelection(tarAssetName("0.4.0", "amd64"), "0.4.0", archive)
			staged := stageData(t, dest, archive, sel, defaultTestLimits())
			_, err := staged.Extract(dest, Identity{GOOS: "linux", GOARCH: "amd64", Flavor: FlavorGUI}.Target(), defaultTestLimits())
			if !errors.Is(err, ErrArchiveInvalid) {
				t.Fatalf("Extract error = %v, want ErrArchiveInvalid", err)
			}
		})
	}
}

func TestExtractRejectsExcessiveEntriesAndExpandedOverflow(t *testing.T) {
	payload := fileBytes(t, fixtureBinary(t, "linux", "amd64", 0, nil, "0.4.0"))
	dest := t.TempDir()

	// Entries cap: one more member than allowed.
	members := happyTarMembers(payload)
	for i := range defaultTestLimits().Entries {
		members = append(members, tarMember{name: "./filler-" + strconv.Itoa(i), data: []byte("x")})
	}
	archive := buildTarGz(t, members)
	sel := testSelection(tarAssetName("0.4.0", "amd64"), "0.4.0", archive)
	staged := stageData(t, dest, archive, sel, defaultTestLimits())
	if _, err := staged.Extract(dest, Identity{GOOS: "linux", GOARCH: "amd64", Flavor: FlavorGUI}.Target(), defaultTestLimits()); !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("entry cap: Extract error = %v, want ErrArchiveInvalid", err)
	}

	// Expanded cap: a member declaring more content than the expanded bound.
	dest2 := t.TempDir()
	huge := append(happyTarMembers(payload), tarMember{name: "./huge", data: make([]byte, defaultTestLimits().Expanded)})
	archive2 := buildTarGz(t, huge)
	sel2 := testSelection(tarAssetName("0.4.0", "amd64"), "0.4.0", archive2)
	staged2 := stageData(t, dest2, archive2, sel2, defaultTestLimits())
	if _, err := staged2.Extract(dest2, Identity{GOOS: "linux", GOARCH: "amd64", Flavor: FlavorGUI}.Target(), defaultTestLimits()); !errors.Is(err, ErrStagingLimit) {
		t.Fatalf("expanded cap: Extract error = %v, want ErrStagingLimit", err)
	}
}

func TestExtractHandlesDirectoryMembersAndNestedPaths(t *testing.T) {
	dest := t.TempDir()
	payload := fileBytes(t, fixtureBinary(t, "linux", "amd64", 0, nil, "0.4.0"))
	members := []tarMember{
		{name: "./torrent-tv", data: payload, mode: 0o755},
		// Nested regular member without an explicit directory entry.
		{name: "./docs/guide.md", data: []byte("guide\n"), mode: 0o644},
		// Explicit directory member plus content beneath it.
		{name: "./share", typ: tar.TypeDir, mode: 0o755},
		{name: "./share/example.txt", data: []byte("example\n"), mode: 0o644},
	}
	archive := buildTarGz(t, members)
	sel := testSelection(tarAssetName("0.4.0", "amd64"), "0.4.0", archive)
	staged := stageData(t, dest, archive, sel, defaultTestLimits())
	payloadResult, err := staged.Extract(dest, Identity{GOOS: "linux", GOARCH: "amd64", Flavor: FlavorGUI}.Target(), defaultTestLimits())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, path := range []string{
		filepath.Join(payloadResult.Dir, "docs", "guide.md"),
		filepath.Join(payloadResult.Dir, "share"),
		filepath.Join(payloadResult.Dir, "share", "example.txt"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("member %s missing after extraction: %v", path, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(payloadResult.Dir, "docs", "guide.md"))
	if err != nil || string(data) != "guide\n" {
		t.Errorf("nested member content = %q, err %v", data, err)
	}

	// The zip path must behave the same for nested regular members without
	// explicit directory entries. (Symlinks are bundle-only; bundle fixtures
	// cannot carry extra members past signing, so the symlink parent-dir guard
	// is not pinned here — real ditto/zip -r output always emits directories.)
	winDest := t.TempDir()
	winPayload := fileBytes(t, fixtureBinary(t, "windows", "amd64", 0, nil, "0.4.0"))
	winArchive := buildZip(t, []zipMember{
		{name: "torrent-tv.exe", data: winPayload},
		{name: "docs/guide.md", data: []byte("guide\n")},
	})
	winSel := testSelection("torrent-tv-0.4.0-windows-amd64-desktop.zip", "0.4.0", winArchive)
	winStaged := stageData(t, winDest, winArchive, winSel, defaultTestLimits())
	winResult, err := winStaged.Extract(winDest, Identity{GOOS: "windows", GOARCH: "amd64", Flavor: FlavorGUI}.Target(), defaultTestLimits())
	if err != nil {
		t.Fatalf("zip nested extraction: %v", err)
	}
	guide, err := os.ReadFile(filepath.Join(winResult.Dir, "docs", "guide.md"))
	if err != nil || string(guide) != "guide\n" {
		t.Errorf("zip nested member = %q, err %v", guide, err)
	}
}

func TestExtractAcceptsZeroSizeMembers(t *testing.T) {
	dest := t.TempDir()
	payload := fileBytes(t, fixtureBinary(t, "linux", "amd64", 0, nil, "0.4.0"))
	archive := buildTarGz(t, []tarMember{
		{name: "./torrent-tv", data: payload, mode: 0o755},
		{name: "./empty-marker", data: nil, mode: 0o644},
	})
	sel := testSelection(tarAssetName("0.4.0", "amd64"), "0.4.0", archive)
	staged := stageData(t, dest, archive, sel, defaultTestLimits())
	payloadResult, err := staged.Extract(dest, Identity{GOOS: "linux", GOARCH: "amd64", Flavor: FlavorGUI}.Target(), defaultTestLimits())
	if err != nil {
		t.Fatalf("tar zero-size member: %v", err)
	}
	info, err := os.Stat(filepath.Join(payloadResult.Dir, "empty-marker"))
	if err != nil {
		t.Fatalf("zero-size member missing: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("zero-size member size = %d, want 0", info.Size())
	}

	// The zip path shares the counting reader: a zero-size member must
	// extract there too.
	winDest := t.TempDir()
	winPayload := fileBytes(t, fixtureBinary(t, "windows", "amd64", 0, nil, "0.4.0"))
	winArchive := buildZip(t, []zipMember{
		{name: "torrent-tv.exe", data: winPayload},
		{name: "empty.txt", data: nil},
	})
	winSel := testSelection("torrent-tv-0.4.0-windows-amd64-desktop.zip", "0.4.0", winArchive)
	winStaged := stageData(t, winDest, winArchive, winSel, defaultTestLimits())
	winResult, err := winStaged.Extract(winDest, Identity{GOOS: "windows", GOARCH: "amd64", Flavor: FlavorGUI}.Target(), defaultTestLimits())
	if err != nil {
		t.Fatalf("zip zero-size member: %v", err)
	}
	if _, err := os.Stat(filepath.Join(winResult.Dir, "empty.txt")); err != nil {
		t.Errorf("zip zero-size member missing: %v", err)
	}
}

func TestCountingLimitReaderStopsAtDeclaredSize(t *testing.T) {
	// A zero-size declaration ends immediately: zero-size members extract.
	empty := &countingLimitReader{r: bytes.NewReader(nil), remaining: 0}
	var sink [1]byte
	if n, err := empty.Read(sink[:]); !errors.Is(err, io.EOF) || n != 0 {
		t.Fatalf("zero declared size: Read = %d, %v; want 0, io.EOF", n, err)
	}

	// Delivery stops at the declared size even when the source offers more;
	// the member checks compare written bytes against the declaration.
	reader := &countingLimitReader{r: bytes.NewReader([]byte(strings.Repeat("x", 100))), remaining: 10}
	var out bytes.Buffer
	if _, err := out.ReadFrom(reader); err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if out.Len() != 10 {
		t.Errorf("delivered %d bytes, want exactly the declared 10", out.Len())
	}
	if _, err := reader.Read(sink[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("Read past declared size = %v, want io.EOF", err)
	}
}

func TestExtractVerifiesExecutableIdentity(t *testing.T) {
	cases := []struct {
		name         string
		goarch       string // the payload's architecture
		targetGoarch string // defaults to goarch
		goarm        int
		tags         []string
		version      string
		selVer       string
		flavor       string
		wantError    error
	}{
		{name: "matching identity accepted", goarch: "amd64", version: "0.4.0", selVer: "0.4.0", flavor: FlavorGUI},
		{name: "absent ldflags accepted for trimpath builds", goarch: "amd64", version: "", selVer: "0.4.0", flavor: FlavorGUI},
		{name: "wrong version rejected", goarch: "amd64", version: "0.3.0", selVer: "0.4.0", flavor: FlavorGUI, wantError: ErrBinaryIdentity},
		{name: "wrong architecture rejected", goarch: "arm64", targetGoarch: "amd64", version: "0.4.0", selVer: "0.4.0", flavor: FlavorGUI, wantError: ErrBinaryIdentity},
		{name: "headless payload rejected for gui flavor", goarch: "amd64", tags: []string{"headless"}, version: "0.4.0", selVer: "0.4.0", flavor: FlavorGUI, wantError: ErrBinaryIdentity},
		{name: "headless payload accepted for headless flavor", goarch: "amd64", tags: []string{"headless"}, version: "0.4.0", selVer: "0.4.0", flavor: FlavorHeadless},
		{name: "armv7 accepted with GOARM 7", goarch: "arm", goarm: 7, version: "0.4.0", selVer: "0.4.0", flavor: FlavorHeadless},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dest := t.TempDir()
			payload := fileBytes(t, fixtureBinary(t, "linux", testCase.goarch, testCase.goarm, testCase.tags, testCase.version))
			members := happyTarMembers(payload)
			if testCase.goarm > 0 {
				members = []tarMember{
					{name: "./torrent-tv", data: payload, mode: 0o755},
				}
			}
			archive := buildTarGz(t, members)
			asset := tarAssetName("0.4.0", testCase.goarch)
			if testCase.goarm > 0 {
				asset = "torrent-tv-0.4.0-linux-armv7-headless.tar.gz"
			}
			sel := testSelection(asset, testCase.selVer, archive)
			staged := stageData(t, dest, archive, sel, defaultTestLimits())
			targetGOARCH := testCase.goarch
			if testCase.targetGoarch != "" {
				targetGOARCH = testCase.targetGoarch
			}
			_, err := staged.Extract(dest, Identity{GOOS: "linux", GOARCH: targetGOARCH, Flavor: testCase.flavor}.Target(), defaultTestLimits())
			if testCase.wantError != nil {
				if !errors.Is(err, testCase.wantError) {
					t.Fatalf("Extract error = %v, want %v", err, testCase.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
		})
	}
}

func TestVerifyExecutableRejectsForeignPlatforms(t *testing.T) {
	sel := testSelection("torrent-tv-0.4.0-linux-amd64-desktop.tar.gz", "0.4.0", nil)
	linuxBinary := fixtureBinary(t, "linux", "amd64", 0, nil, "0.4.0")
	windowsBinary := fixtureBinary(t, "windows", "amd64", 0, nil, "0.4.0")
	if err := VerifyExecutable(linuxBinary, sel, Identity{GOOS: "linux", GOARCH: "amd64", Flavor: FlavorGUI}.Target()); err != nil {
		t.Errorf("linux/amd64 target: %v", err)
	}
	if err := VerifyExecutable(windowsBinary, sel, Identity{GOOS: "windows", GOARCH: "amd64", Flavor: FlavorGUI}.Target()); err != nil {
		t.Errorf("windows/amd64 target: %v", err)
	}
	if err := VerifyExecutable(linuxBinary, sel, Identity{GOOS: "windows", GOARCH: "amd64", Flavor: FlavorGUI}.Target()); !errors.Is(err, ErrBinaryIdentity) {
		t.Errorf("linux binary for windows target error = %v, want ErrBinaryIdentity", err)
	}
}

func TestInjectedVersionParsing(t *testing.T) {
	flags := "-s -w -X " + compositionPath + ".Version=1.2.3"
	version, ok := injectedVersion(flags)
	if !ok || version != "1.2.3" {
		t.Errorf("injectedVersion(%q) = %q, %v; want 1.2.3, true", flags, version, ok)
	}
	if _, ok := injectedVersion("-s -w"); ok {
		t.Error("injectedVersion found a version without an -X flag")
	}
}

// The bundle tests need darwin tooling (lipo, codesign) and run only where
// the release bundle is meaningful.
func bundleTestCapable(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("macOS bundle verification requires a darwin host")
	}
}

// universalFixture builds a universal (fat) fixture binary for the darwin
// bundle payload.
func universalFixture(t *testing.T) []byte {
	t.Helper()
	arm64Bin := fixtureBinary(t, "darwin", "arm64", 0, nil, "0.4.0")
	amd64Bin := fixtureBinary(t, "darwin", "amd64", 0, nil, "0.4.0")
	fat := filepath.Join(filepath.Dir(arm64Bin), "universal-0-4-0")
	if _, err := os.Stat(fat); err != nil {
		cmd := exec.Command("lipo", "-create", "-output", fat, arm64Bin, amd64Bin)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("lipo: %v\n%s", err, output)
		}
	}
	return fileBytes(t, fat)
}

// signedBundleZip builds a zip of an ad-hoc signed .app bundle carrying the
// universal fixture binary, mirroring the release bundle shape.
func signedBundleZip(t *testing.T, version, identifier string, sign bool, extraMembers []zipMember) []byte {
	t.Helper()
	bundleRoot := t.TempDir()
	appDir := filepath.Join(bundleRoot, "Torrent TV.app")
	contents := filepath.Join(appDir, "Contents")
	macOS := filepath.Join(contents, "MacOS")
	if err := os.MkdirAll(macOS, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleExecutable</key><string>torrent-tv</string>
	<key>CFBundleIdentifier</key><string>%s</string>
	<key>CFBundleVersion</key><string>%s</string>
	<key>CFBundleShortVersionString</key><string>%s</string>
</dict></plist>`, identifier, version, version)
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(macOS, "torrent-tv"), universalFixture(t), 0o755); err != nil {
		t.Fatal(err)
	}
	// A framework-style versioned directory with the canonical Current -> A
	// symlink: the internal link the bundle validation must preserve.
	versions := filepath.Join(contents, "Resources", "Versions")
	if err := os.MkdirAll(filepath.Join(versions, "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versions, "A", "placeholder"), []byte("framework resource\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("A", filepath.Join(versions, "Current")); err != nil {
		t.Fatal(err)
	}
	if sign {
		cmd := exec.Command("codesign", "--force", "--deep", "--sign", "-", appDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("codesign fixture bundle: %v\n%s", err, output)
		}
	}
	members := zipTree(t, appDir, "Torrent TV.app")
	members = append(members, extraMembers...)
	return buildZip(t, members)
}

// zipTree walks root and renders every file, directory, and symlink under
// it as archive members named relative to root, prefixed with prefix. This
// captures signature files (_CodeSignature) exactly as codesign wrote them.
func zipTree(t *testing.T, root, prefix string) []zipMember {
	t.Helper()
	var members []zipMember
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			// Keep the .app root itself as an explicit directory entry so
			// extraction creates it before any member lands inside.
			members = append(members, zipMember{name: prefix, dir: true})
			return nil
		}
		name := prefix + "/" + filepath.ToSlash(rel)
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return linkErr
			}
			members = append(members, zipMember{name: name, link: target})
		case entry.IsDir():
			members = append(members, zipMember{name: name, dir: true})
		default:
			members = append(members, zipMember{name: name, data: fileBytes(t, path)})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk bundle: %v", err)
	}
	return members
}

func bundleSelection(content []byte) Selection {
	return testSelection("torrent-tv-0.4.0-macos-universal-app.zip", "0.4.0", content)
}

func bundleTarget() Target {
	return Identity{GOOS: "darwin", GOARCH: "arm64", Flavor: FlavorBundle}.Target()
}

func TestExtractVerifiesAndAcceptsSignedBundle(t *testing.T) {
	bundleTestCapable(t)
	dest := t.TempDir()
	archive := signedBundleZip(t, "0.4.0", bundleIdentifier, true, nil)
	staged := stageData(t, dest, archive, bundleSelection(archive), defaultTestLimits())

	payloadResult, err := staged.Extract(dest, bundleTarget(), defaultTestLimits())
	if err != nil {
		t.Fatalf("Extract bundle: %v", err)
	}
	if payloadResult.Kind != PayloadBundle || payloadResult.Bundle == "" {
		t.Fatalf("payload = %+v, want bundle", payloadResult)
	}
	info, err := readBundleInfo(filepath.Join(payloadResult.Bundle, "Contents", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Identifier != bundleIdentifier || info.ShortVersion != "0.4.0" {
		t.Errorf("bundle identity = %+v", info)
	}
}

func TestExtractRejectsBundleIdentityMismatches(t *testing.T) {
	bundleTestCapable(t)
	cases := []struct {
		name       string
		version    string
		identifier string
		sign       bool
		extra      []zipMember
		wantErr    error
	}{
		{name: "wrong plist version", version: "0.3.0", identifier: bundleIdentifier, sign: true, wantErr: ErrBinaryIdentity},
		{name: "wrong identifier", version: "0.4.0", identifier: "com.evil.app", sign: true, wantErr: ErrBinaryIdentity},
		{name: "unsigned bundle", version: "0.4.0", identifier: bundleIdentifier, sign: false, wantErr: ErrBinaryIdentity},
		{
			name: "escaping framework symlink", version: "0.4.0", identifier: bundleIdentifier, sign: true,
			extra: []zipMember{{name: "Torrent TV.app/Contents/Frameworks/Evil", link: "../../../../../../etc/passwd"}},
			// The escaping symlink invalidates the signature too, but member
			// validation must reject it before signature checks run.
			wantErr: ErrArchiveInvalid,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dest := t.TempDir()
			archive := signedBundleZip(t, testCase.version, testCase.identifier, testCase.sign, testCase.extra)
			staged := stageData(t, dest, archive, bundleSelection(archive), defaultTestLimits())
			_, err := staged.Extract(dest, bundleTarget(), defaultTestLimits())
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("Extract error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

func TestExtractAcceptsInBundleSymlinkOnlyForBundleFlavor(t *testing.T) {
	bundleTestCapable(t)
	dest := t.TempDir()
	// The signed happy-path bundle contains Resources/Versions/Current -> A,
	// which must survive extraction as a symlink inside the bundle.
	archive := signedBundleZip(t, "0.4.0", bundleIdentifier, true, nil)
	staged := stageData(t, dest, archive, bundleSelection(archive), defaultTestLimits())
	payloadResult, err := staged.Extract(dest, bundleTarget(), defaultTestLimits())
	if err != nil {
		t.Fatalf("Extract bundle: %v", err)
	}
	link := filepath.Join(payloadResult.Bundle, "Contents", "Resources", "Versions", "Current")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("framework symlink missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s is not a symlink", link)
	}
	if target, err := os.Readlink(link); err != nil || target != "A" {
		t.Errorf("symlink target = %q, err %v; want A", target, err)
	}
}

func TestStageRejectsInvalidSelectionChecksum(t *testing.T) {
	dest := t.TempDir()
	sel := testSelection("asset.tar.gz", "0.4.0", []byte("data"))
	sel.SHA256 = "not-hex"
	if _, err := StageArchive(dest, sel, bytes.NewReader([]byte("data")), defaultTestLimits()); err == nil {
		t.Fatal("StageArchive accepted a selection with a malformed checksum")
	}
}

func TestReadBundleInfoParsesIdentityFields(t *testing.T) {
	plist := `<?xml version="1.0"?><plist><dict>
		<key>CFBundleIdentifier</key><string>com.torrent-tv.app</string>
		<key>CFBundleExecutable</key><string>torrent-tv</string>
		<key>CFBundleVersion</key><string>9.9.9</string>
		<key>CFBundleShortVersionString</key><string>9.9.9</string>
		<key>Unrelated</key><string>ignored</string>
	</dict></plist>`
	path := filepath.Join(t.TempDir(), "Info.plist")
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := readBundleInfo(path)
	if err != nil {
		t.Fatalf("readBundleInfo: %v", err)
	}
	if info != (bundleInfo{Identifier: "com.torrent-tv.app", Executable: "torrent-tv", Version: "9.9.9", ShortVersion: "9.9.9"}) {
		t.Errorf("bundleInfo = %+v", info)
	}
}

package updates

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Staging and extraction bounds. There is deliberately no minimum size: a
// release asset is as large as the release publishes and not one byte more.
const (
	// maxCompressedBytes caps the compressed download stream at 512 MiB.
	maxCompressedBytes int64 = 512 << 20
	// maxExpandedBytes caps the cumulative expanded archive content at 1 GiB.
	maxExpandedBytes int64 = 1 << 30
	// maxArchiveEntries bounds the member count of one archive. Real release
	// payloads hold a handful of members; anything near this bound is hostile.
	maxArchiveEntries = 4096

	// payloadBaseName is the executable name inside every file-flavor release
	// archive, before the Windows ".exe" suffix.
	payloadBaseName = "torrent-tv"

	// bundleSuffix marks a macOS .app payload directory.
	bundleSuffix = ".app"

	// bundleIdentifier is the fixed CFBundleIdentifier of the macOS bundle
	// release asset (build/darwin/Info.plist).
	bundleIdentifier = "com.torrenttv.app"

	// compositionPath is the package carrying the linker-injected version in
	// release builds (-X .../internal/composition.Version=<version>).
	compositionPath = "github.com/mihaiflorentin88/torrent-tv/internal/composition"
)

// Staging and verification failure classes. Callers classify with errors.Is.
var (
	// ErrStagingLimit marks a download or archive that exceeds the size or
	// member bounds.
	ErrStagingLimit = errors.New("updates: staged release exceeds size bounds")

	// ErrChecksumMismatch marks a completed download whose digest differs
	// from the release manifest entry.
	ErrChecksumMismatch = errors.New("updates: staged download does not match release checksum")

	// ErrArchiveInvalid marks archive members outside the expected release
	// payload shape: links where none are allowed, devices, traversal,
	// absolute names, duplicates, or excess entries.
	ErrArchiveInvalid = errors.New("updates: archive content rejected")

	// ErrBinaryIdentity marks an extracted payload whose executable does not
	// match the target platform or the selected build identity.
	ErrBinaryIdentity = errors.New("updates: staged executable identity mismatch")

	// ErrUnsupportedArchive marks an asset that is neither a published tar.gz
	// nor zip release archive.
	ErrUnsupportedArchive = errors.New("updates: unsupported release archive format")
)

// Limits bounds one staging/extraction transaction. DefaultLimits returns the
// published release bounds; tests shrink them to exercise failure paths.
type Limits struct {
	Compressed int64 // maximum compressed download bytes
	Expanded   int64 // maximum cumulative expanded bytes
	Entries    int   // maximum archive members
}

// DefaultLimits returns the release bounds: 512 MiB compressed, 1 GiB
// expanded, bounded member count.
func DefaultLimits() Limits {
	return Limits{Compressed: maxCompressedBytes, Expanded: maxExpandedBytes, Entries: maxArchiveEntries}
}

// Target is the platform and flavor an extracted payload must match.
type Target struct {
	GOOS   string
	GOARCH string
	GOARM  int
	Flavor string
}

// Target resolves the verification target for an installation identity.
func (i Identity) Target() Target {
	arm := 0
	if i.GOOS == "linux" && i.GOARCH == "arm" {
		arm = 7
	}
	return Target{GOOS: i.GOOS, GOARCH: i.GOARCH, GOARM: arm, Flavor: i.Flavor}
}

// Staged is a checksum-verified release archive held on the destination
// filesystem, ready for extraction.
type Staged struct {
	Selection Selection
	Path      string            // unique mode-0600 file on the destination filesystem
	Digest    [sha256.Size]byte // the verified asset digest
}

// PayloadKind distinguishes a single-file payload from a macOS .app bundle.
type PayloadKind int

const (
	PayloadFile PayloadKind = iota
	PayloadBundle
)

// Payload is verified update content extracted into a unique staging
// directory on the destination filesystem.
type Payload struct {
	Dir        string      // extraction root
	Kind       PayloadKind // file or bundle payload
	Executable string      // verified executable path (PayloadFile)
	Bundle     string      // verified .app bundle directory (PayloadBundle)
}

// StageArchive streams body into a unique mode-0600 file on destDir's
// filesystem, hashing while writing and enforcing limits.Compressed. The
// completed digest must equal sel.SHA256 before the file is accepted; every
// failure removes the partial file. The body is never buffered in memory and
// never re-read: the digest is computed over the bytes as they land on disk.
func StageArchive(destDir string, sel Selection, body io.Reader, limits Limits) (*Staged, error) {
	if !hexDigest(sel.SHA256) {
		return nil, fmt.Errorf("stage: selection checksum %q is not 64 hexadecimal digits", sel.SHA256)
	}
	if limits.Compressed <= 0 || limits.Expanded <= 0 || limits.Entries <= 0 {
		return nil, fmt.Errorf("stage: invalid limits %+v", limits)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("stage: prepare destination: %w", err)
	}
	file, err := os.CreateTemp(destDir, ".filelist-stage-*.part")
	if err != nil {
		return nil, fmt.Errorf("stage: create staging file: %w", err)
	}
	stagedPath := file.Name()
	remove := true
	defer func() {
		if remove {
			file.Close()
			os.Remove(stagedPath)
		}
	}()

	digest := sha256.New()
	bounded := &boundedReader{r: body, remaining: limits.Compressed}
	// io.Copy streams through a small window; nothing retains the payload.
	if _, err := io.Copy(io.MultiWriter(file, digest), bounded); err != nil {
		if errors.Is(err, errOverLimit) {
			return nil, fmt.Errorf("%w: compressed download exceeds %d bytes", ErrStagingLimit, limits.Compressed)
		}
		return nil, fmt.Errorf("stage: stream download: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("stage: flush staging file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("stage: close staging file: %w", err)
	}
	sum := digest.Sum(nil)
	if !sameDigest(sum, sel.SHA256) {
		return nil, fmt.Errorf("%w: asset %s hashed %s, release manifest says %s",
			ErrChecksumMismatch, sel.AssetName, hex.EncodeToString(sum), sel.SHA256)
	}
	remove = false
	return &Staged{Selection: sel, Path: stagedPath, Digest: [sha256.Size]byte(sum)}, nil
}

// errOverLimit signals the compressed stream crossed its bound.
var errOverLimit = errors.New("bound exceeded")

// boundedReader enforces the compressed cap during streaming.
type boundedReader struct {
	r         io.Reader
	remaining int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.remaining < 0 {
		return 0, errOverLimit
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining+1]
	}
	n, err := b.r.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		return n, errOverLimit
	}
	return n, err
}

// sameDigest compares a raw digest with a hexadecimal selection checksum.
func sameDigest(sum []byte, expected string) bool {
	if len(sum) != sha256.Size {
		return false
	}
	return hex.EncodeToString(sum) == strings.ToLower(expected)
}

// Extract validates every archive member against the expected release payload
// shape and unpacks the archive into a unique staging directory under
// destDir, then verifies the payload identity for target. Every failure
// removes the staging directory; nothing outside it is ever written.
func (s *Staged) Extract(destDir string, target Target, limits Limits) (*Payload, error) {
	if limits.Compressed <= 0 || limits.Expanded <= 0 || limits.Entries <= 0 {
		return nil, fmt.Errorf("extract: invalid limits %+v", limits)
	}
	dir, err := os.MkdirTemp(destDir, ".filelist-extract-*")
	if err != nil {
		return nil, fmt.Errorf("extract: create staging directory: %w", err)
	}
	payload, err := s.extractInto(dir, target, limits)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return payload, nil
}

func (s *Staged) extractInto(dir string, target Target, limits Limits) (*Payload, error) {
	switch {
	case strings.HasSuffix(s.Selection.AssetName, ".tar.gz"):
		if err := extractTarGz(s.Path, dir, target, limits); err != nil {
			return nil, err
		}
	case strings.HasSuffix(s.Selection.AssetName, ".zip"):
		if err := extractZip(s.Path, dir, target, limits); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: asset %q", ErrUnsupportedArchive, s.Selection.AssetName)
	}
	return verifyPayload(dir, s.Selection, target)
}

// archiveRules accumulates per-archive validation state.
type archiveRules struct {
	root       string
	target     Target
	limits     Limits
	seen       map[string]bool
	expanded   int64
	bundleName string // root .app member name (bundle flavor only)
	entries    int
}

func newArchiveRules(root string, target Target, limits Limits) *archiveRules {
	return &archiveRules{root: root, target: target, limits: limits, seen: map[string]bool{}}
}

// memberPath validates one archive member name and returns its destination
// path. It rejects absolute names, traversal, backslashes, duplicates, and
// excess entries. The archive root entry itself yields an empty destination.
func (r *archiveRules) memberPath(name string) (string, error) {
	r.entries++
	if r.entries > r.limits.Entries {
		return "", fmt.Errorf("%w: more than %d archive members", ErrArchiveInvalid, r.limits.Entries)
	}
	if name == "" {
		return "", fmt.Errorf("%w: empty member name", ErrArchiveInvalid)
	}
	if strings.ContainsAny(name, "\\\x00") {
		return "", fmt.Errorf("%w: member %q has a separator outside the archive convention", ErrArchiveInvalid, name)
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%w: absolute member name %q", ErrArchiveInvalid, name)
	}
	clean := cleanArchiveName(name)
	if clean == "." {
		return "", nil // the archive root entry
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: member %q escapes the extraction root", ErrArchiveInvalid, name)
		}
	}
	if r.seen[clean] {
		return "", fmt.Errorf("%w: duplicate member %q", ErrArchiveInvalid, clean)
	}
	r.seen[clean] = true
	if err := r.markBundle(clean); err != nil {
		return "", err
	}
	return filepath.Join(r.root, filepath.FromSlash(clean)), nil
}

// markBundle enforces the bundle-flavor payload shape: every member lives
// inside exactly one root .app directory, and no other flavor carries .app
// content at all.
func (r *archiveRules) markBundle(clean string) error {
	first := firstPathElement(clean)
	if !strings.HasSuffix(first, bundleSuffix) {
		if r.target.Flavor == FlavorBundle {
			return fmt.Errorf("%w: member %q outside the bundle directory", ErrArchiveInvalid, clean)
		}
		return nil
	}
	if r.target.Flavor != FlavorBundle {
		return fmt.Errorf("%w: member %q: only the bundle flavor carries .app content", ErrArchiveInvalid, clean)
	}
	if r.bundleName == "" {
		r.bundleName = first
	}
	if r.bundleName != first {
		return fmt.Errorf("%w: multiple .app directories in archive", ErrArchiveInvalid)
	}
	return nil
}

// accountSize reserves expanded size for one member before extraction and
// enforces the cumulative cap against declared header sizes.
func (r *archiveRules) accountSize(size int64, name string) error {
	if size < 0 {
		return fmt.Errorf("%w: member %q declares negative size", ErrArchiveInvalid, name)
	}
	if r.expanded+size > r.limits.Expanded {
		return fmt.Errorf("%w: expanded content exceeds %d bytes", ErrStagingLimit, r.limits.Expanded)
	}
	r.expanded += size
	return nil
}

// checkLinkTarget validates a symlink member: links exist only inside the
// macOS bundle payload, never target absolute paths, and never resolve
// outside the extracted bundle.
func (r *archiveRules) checkLinkTarget(memberClean, linkName string) error {
	first := firstPathElement(memberClean)
	if r.target.Flavor != FlavorBundle || !strings.HasSuffix(first, bundleSuffix) {
		return fmt.Errorf("%w: symlink %q outside a macOS bundle", ErrArchiveInvalid, memberClean)
	}
	if path.IsAbs(linkName) {
		return fmt.Errorf("%w: symlink %q targets absolute path %q", ErrArchiveInvalid, memberClean, linkName)
	}
	resolved := path.Clean(path.Join(path.Dir(memberClean), linkName))
	if resolved == "." || firstPathElement(resolved) != first {
		return fmt.Errorf("%w: symlink %q resolves outside the bundle", ErrArchiveInvalid, memberClean)
	}
	return nil
}

func firstPathElement(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

// extractTarGz unpacks a verified .tar.gz release archive under dir.
func extractTarGz(archivePath, dir string, target Target, limits Limits) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("extract: open staged archive: %w", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("extract: read gzip stream: %w", err)
	}
	defer gz.Close()
	rules := newArchiveRules(dir, target, limits)
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			// The tar stream ended; the gzip wrapper must also end cleanly.
			// A missing or corrupt trailer means the download was truncated
			// inside the final padding, which member parsing cannot see.
			var probe [1]byte
			if _, err := gz.Read(probe[:]); !errors.Is(err, io.EOF) {
				return fmt.Errorf("%w: gzip stream ends prematurely: %v", ErrArchiveInvalid, err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: read archive member: %v", ErrArchiveInvalid, err)
		}
		dest, err := rules.memberPath(header.Name)
		if err != nil {
			return err
		}
		if dest == "" {
			continue // archive root entry
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, os.FileMode(header.Mode)&0o777|0o700); err != nil {
				return fmt.Errorf("extract: create directory: %w", err)
			}
		case tar.TypeReg:
			if err := rules.accountSize(header.Size, header.Name); err != nil {
				return err
			}
			// Nested members may arrive without explicit directory entries.
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return fmt.Errorf("extract: create member directory: %w", err)
			}
			if err := writeFileVerified(dest, reader, header.Size, os.FileMode(header.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := rules.checkLinkTarget(cleanArchiveName(header.Name), header.Linkname); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return fmt.Errorf("extract: create symlink directory: %w", err)
			}
			if err := os.Symlink(header.Linkname, dest); err != nil {
				return fmt.Errorf("extract: create symlink: %w", err)
			}
		default:
			return fmt.Errorf("%w: member %q is not a regular file or directory", ErrArchiveInvalid, header.Name)
		}
	}
}

// extractZip unpacks a verified .zip release archive under dir.
func extractZip(archivePath, dir string, target Target, limits Limits) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("%w: read zip archive: %v", ErrArchiveInvalid, err)
	}
	defer reader.Close()
	rules := newArchiveRules(dir, target, limits)
	for _, entry := range reader.File {
		name := strings.TrimSuffix(entry.Name, "/")
		dest, err := rules.memberPath(name)
		if err != nil {
			return err
		}
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 {
			linkName, err := readZipSymlink(entry)
			if err != nil {
				return err
			}
			if err := rules.checkLinkTarget(cleanArchiveName(name), linkName); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return fmt.Errorf("extract: create parent directory: %w", err)
			}
			if err := os.Symlink(linkName, dest); err != nil {
				return fmt.Errorf("extract: create symlink: %w", err)
			}
			continue
		}
		if mode&(os.ModeNamedPipe|os.ModeDevice|os.ModeSocket) != 0 {
			return fmt.Errorf("%w: member %q is not a regular file or directory", ErrArchiveInvalid, entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return fmt.Errorf("extract: create directory: %w", err)
			}
			continue
		}
		if err := rules.accountSize(int64(entry.UncompressedSize64), entry.Name); err != nil {
			return err
		}
		// Nested members may arrive without explicit directory entries.
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("extract: create member directory: %w", err)
		}
		src, err := entry.Open()
		if err != nil {
			return fmt.Errorf("%w: open member %q: %v", ErrArchiveInvalid, entry.Name, err)
		}
		err = writeFileVerified(dest, src, int64(entry.UncompressedSize64), mode&os.ModePerm)
		src.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// readZipSymlink reads the link target stored as file content.
func readZipSymlink(entry *zip.File) (string, error) {
	src, err := entry.Open()
	if err != nil {
		return "", fmt.Errorf("%w: read symlink %q: %v", ErrArchiveInvalid, entry.Name, err)
	}
	defer src.Close()
	target, err := io.ReadAll(io.LimitReader(src, 4096))
	if err != nil {
		return "", fmt.Errorf("%w: read symlink %q: %v", ErrArchiveInvalid, entry.Name, err)
	}
	return string(target), nil
}

// writeFileVerified streams one member to disk. The expanded cap is enforced
// against actual bytes via the declared size the archive reader itself
// honors, and a member that delivers anything other than its declared size
// is rejected.
func writeFileVerified(dest string, src io.Reader, declared int64, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	file, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("extract: create member: %w", err)
	}
	written, err := io.Copy(file, &countingLimitReader{r: src, remaining: declared})
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(dest)
		return fmt.Errorf("extract: stream member: %w", err)
	}
	if written != declared {
		os.Remove(dest)
		return fmt.Errorf("%w: member delivered %d bytes for %d declared", ErrArchiveInvalid, written, declared)
	}
	return nil
}

// countingLimitReader rejects members delivering more bytes than declared.
type countingLimitReader struct {
	r         io.Reader
	remaining int64
}

func (c *countingLimitReader) Read(p []byte) (int, error) {
	if c.remaining < 0 {
		return 0, fmt.Errorf("%w: member exceeds its declared size", ErrArchiveInvalid)
	}
	if c.remaining == 0 {
		// Exactly the declared size was delivered: zero-size members end
		// here, and over-delivery beyond a non-zero declaration is caught
		// by the reader itself and the written!=declared check.
		return 0, io.EOF
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	return n, err
}

func cleanArchiveName(name string) string {
	return strings.TrimPrefix(path.Clean(strings.TrimSuffix(name, "/")), "./")
}

// verifyPayload locates and verifies the update payload inside the extraction
// directory: a single platform executable for file flavors, or the .app
// bundle identity, signature, and inner executable for the bundle flavor.
func verifyPayload(dir string, sel Selection, target Target) (*Payload, error) {
	if target.Flavor == FlavorBundle {
		bundle, err := findBundleDir(dir)
		if err != nil {
			return nil, err
		}
		if err := verifyBundle(bundle, sel, target); err != nil {
			return nil, err
		}
		return &Payload{Dir: dir, Kind: PayloadBundle, Bundle: bundle}, nil
	}
	name := payloadBaseName
	if target.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(dir, name)
	info, err := os.Lstat(executable)
	if err != nil {
		return nil, fmt.Errorf("%w: payload executable %q absent", ErrArchiveInvalid, name)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: payload executable %q is not a regular file", ErrArchiveInvalid, name)
	}
	if err := os.Chmod(executable, 0o755); err != nil {
		return nil, fmt.Errorf("extract: make payload executable: %w", err)
	}
	if err := VerifyExecutable(executable, sel, target); err != nil {
		return nil, err
	}
	return &Payload{Dir: dir, Kind: PayloadFile, Executable: executable}, nil
}

// findBundleDir locates the single root .app directory.
func findBundleDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("extract: read staging directory: %w", err)
	}
	var found []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), bundleSuffix) && entry.IsDir() {
			found = append(found, entry.Name())
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("%w: expected exactly one %s directory, found %d", ErrArchiveInvalid, bundleSuffix, len(found))
	}
	return filepath.Join(dir, found[0]), nil
}

// bundleInfo carries the identity fields verified inside a staged .app.
type bundleInfo struct {
	Identifier   string
	Executable   string
	Version      string
	ShortVersion string
}

// verifyBundle checks the staged bundle identity (Info.plist fields), the
// inner executable's Mach-O identity, and the code signature. Signature
// verification never re-signs and never touches quarantine attributes.
func verifyBundle(bundle string, sel Selection, target Target) error {
	if target.GOOS != "darwin" {
		return fmt.Errorf("%w: bundle flavor requires darwin, got %s", ErrBinaryIdentity, target.GOOS)
	}
	info, err := readBundleInfo(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		return err
	}
	if info.Identifier != bundleIdentifier {
		return fmt.Errorf("%w: bundle identifier %q, expected %q", ErrBinaryIdentity, info.Identifier, bundleIdentifier)
	}
	if info.ShortVersion != sel.Version || info.Version != sel.Version {
		return fmt.Errorf("%w: bundle versions (%s, %s) do not match release %s", ErrBinaryIdentity, info.ShortVersion, info.Version, sel.Version)
	}
	executable := filepath.Join(bundle, "Contents", "MacOS", info.Executable)
	infoStat, err := os.Lstat(executable)
	if err != nil || !infoStat.Mode().IsRegular() {
		return fmt.Errorf("%w: bundle executable %q absent", ErrBinaryIdentity, info.Executable)
	}
	if err := VerifyExecutable(executable, sel, target); err != nil {
		return err
	}
	if verifyBundleSignature == nil {
		return fmt.Errorf("%w: signature verification unavailable on this platform", ErrBinaryIdentity)
	}
	if err := verifyBundleSignature(bundle); err != nil {
		return fmt.Errorf("%w: codesign verification failed: %v", ErrBinaryIdentity, err)
	}
	return nil
}

// verifyBundleSignature verifies a staged bundle's code signature. It is
// bound by install_darwin.go to `codesign --verify --deep --strict`; other
// platforms leave it nil.
var verifyBundleSignature func(bundle string) error

// readBundleInfo parses the identity dict from a bundle Info.plist. Only the
// four string keys the update path depends on are extracted.
func readBundleInfo(plistPath string) (bundleInfo, error) {
	data, err := os.ReadFile(plistPath)
	if err != nil {
		return bundleInfo{}, fmt.Errorf("%w: read bundle Info.plist: %v", ErrBinaryIdentity, err)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	var info bundleInfo
	var currentKey string
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return bundleInfo{}, fmt.Errorf("%w: parse bundle Info.plist: %v", ErrBinaryIdentity, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			if element.Name.Local != "key" && element.Name.Local != "string" {
				continue
			}
			var text string
			if err := decoder.DecodeElement(&text, &element); err != nil {
				return bundleInfo{}, fmt.Errorf("%w: parse bundle Info.plist value: %v", ErrBinaryIdentity, err)
			}
			if element.Name.Local == "key" {
				currentKey = text
				continue
			}
			switch currentKey {
			case "CFBundleIdentifier":
				info.Identifier = text
			case "CFBundleExecutable":
				info.Executable = text
			case "CFBundleVersion":
				info.Version = text
			case "CFBundleShortVersionString":
				info.ShortVersion = text
			}
			currentKey = ""
		}
	}
	if info.Identifier == "" || info.Executable == "" || info.Version == "" || info.ShortVersion == "" {
		return bundleInfo{}, fmt.Errorf("%w: bundle Info.plist missing identity fields", ErrBinaryIdentity)
	}
	return info, nil
}

// VerifyExecutable checks an extracted executable against the target
// platform and the selected release identity. It reads format headers and
// the embedded Go build information only; it never executes the content.
//
// Architecture: the ELF/PE/Mach-O header machine type must match the target,
// and the embedded GOOS/GOARCH must agree (single-arch payloads; the bundle
// flavor's universal binary is validated by fat-header slice containment).
// Flavor: a `headless` build tag must never appear in a gui/bundle payload
// (the armv7 headless build carries no tag, so its absence proves nothing).
// Version: when the build recorded its -ldflags, the injected
// composition.Version must equal the selection; -trimpath builds (all
// published release binaries) omit that setting, and for them the version
// identity is carried by the manifest checksum chain plus the bundle
// Info.plist.
func VerifyExecutable(path string, sel Selection, target Target) error {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: read embedded build info: %v", ErrBinaryIdentity, err)
	}
	settings := map[string]string{}
	for _, setting := range bi.Settings {
		settings[setting.Key] = setting.Value
	}
	if goos := settings["GOOS"]; goos != target.GOOS {
		return fmt.Errorf("%w: embedded GOOS %q, expected %q", ErrBinaryIdentity, goos, target.GOOS)
	}
	if err := checkHeaderMachine(path, target); err != nil {
		return err
	}
	if goarch := settings["GOARCH"]; goarch != target.GOARCH && !universalBundle(path, target) {
		return fmt.Errorf("%w: embedded GOARCH %q, expected %q", ErrBinaryIdentity, goarch, target.GOARCH)
	}
	if target.GOOS == "linux" && target.GOARCH == "arm" && settings["GOARM"] != "7" {
		return fmt.Errorf("%w: embedded GOARM %q, expected 7", ErrBinaryIdentity, settings["GOARM"])
	}
	if tags := settings["-tags"]; strings.Contains(tags, "headless") && target.Flavor != FlavorHeadless {
		return fmt.Errorf("%w: payload built with headless tag for %s flavor", ErrBinaryIdentity, target.Flavor)
	}
	if version, ok := injectedVersion(settings["-ldflags"]); ok && version != normalizeVersion(sel.Version) {
		return fmt.Errorf("%w: embedded version %q, expected %q", ErrBinaryIdentity, version, normalizeVersion(sel.Version))
	}
	return nil
}

// checkHeaderMachine validates the executable format header machine type for
// the target platform.
func checkHeaderMachine(path string, target Target) error {
	switch target.GOOS {
	case "linux":
		f, err := elf.Open(path)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrBinaryIdentity, err)
		}
		defer f.Close()
		want, ok := map[string]elf.Machine{
			"amd64": elf.EM_X86_64,
			"arm64": elf.EM_AARCH64,
			"arm":   elf.EM_ARM,
		}[target.GOARCH]
		if !ok {
			return fmt.Errorf("%w: unsupported linux arch %q", ErrBinaryIdentity, target.GOARCH)
		}
		if f.Machine != want {
			return fmt.Errorf("%w: ELF machine %v, expected %v", ErrBinaryIdentity, f.Machine, want)
		}
	case "windows":
		f, err := pe.Open(path)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrBinaryIdentity, err)
		}
		defer f.Close()
		want, ok := map[string]uint16{
			"amd64": 0x8664,
			"arm64": 0xaa64,
		}[target.GOARCH]
		if !ok {
			return fmt.Errorf("%w: unsupported windows arch %q", ErrBinaryIdentity, target.GOARCH)
		}
		if f.Machine != want {
			return fmt.Errorf("%w: PE machine %#x, expected %#x", ErrBinaryIdentity, f.Machine, want)
		}
	case "darwin":
		return checkMachOArch(path, target)
	default:
		return fmt.Errorf("%w: unsupported target platform %q", ErrBinaryIdentity, target.GOOS)
	}
	return nil
}

// checkMachOArch validates Mach-O (or universal fat) architecture identity.
func checkMachOArch(path string, target Target) error {
	fat, fatErr := macho.OpenFat(path)
	if fatErr == nil {
		defer fat.Close()
		if target.Flavor != FlavorBundle {
			return fmt.Errorf("%w: universal binary in a single-arch payload", ErrBinaryIdentity)
		}
		want := machoCPU(target.GOARCH)
		for _, slice := range fat.Arches {
			if slice.Cpu == want {
				return nil
			}
		}
		return fmt.Errorf("%w: universal binary lacks %s slice", ErrBinaryIdentity, target.GOARCH)
	}
	if !errors.Is(fatErr, macho.ErrNotFat) {
		return fmt.Errorf("%w: %s", ErrBinaryIdentity, fatErr)
	}
	f, err := macho.Open(path)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrBinaryIdentity, err)
	}
	defer f.Close()
	if f.Cpu != machoCPU(target.GOARCH) {
		return fmt.Errorf("%w: Mach-O cpu %v, expected %s", ErrBinaryIdentity, f.Cpu, target.GOARCH)
	}
	return nil
}

func machoCPU(goarch string) macho.Cpu {
	switch goarch {
	case "amd64":
		return macho.CpuAmd64
	case "arm64":
		return macho.CpuArm64
	}
	return 0
}

// universalBundle reports whether path is a universal Mach-O binary, which
// is only acceptable for the bundle flavor's darwin payload.
func universalBundle(path string, target Target) bool {
	if target.GOOS != "darwin" || target.Flavor != FlavorBundle {
		return false
	}
	fat, err := macho.OpenFat(path)
	if err != nil {
		return false
	}
	fat.Close()
	return true
}

// injectedVersion extracts the linker-injected composition version from a
// recorded -ldflags build setting, if any. Both `-X name=value` and
// `-X=name=value` spellings appear in recorded flags.
func injectedVersion(ldflags string) (string, bool) {
	fields := strings.Fields(ldflags)
	for i, field := range fields {
		var spec string
		switch {
		case field == "-X" && i+1 < len(fields):
			spec = fields[i+1]
		case strings.HasPrefix(field, "-X="):
			spec = strings.TrimPrefix(field, "-X=")
		default:
			continue
		}
		// The -X spec is "<import path>.<variable>=<value>".
		if !strings.HasPrefix(spec, compositionPath+".") {
			continue
		}
		_, value, found := strings.Cut(spec[len(compositionPath)+1:], "=")
		if !found {
			continue
		}
		return value, true
	}
	return "", false
}

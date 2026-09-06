package composition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/filelist"
	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/httpapi"
	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/mediaprobe"
	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/nativetorrent"
	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/portalclient"
	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/qbittorrent"
	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/subtitles"
	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/tmdb"
	"github.com/mihaiflorentin88/torrent-tv/internal/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/application/portal"
	"github.com/mihaiflorentin88/torrent-tv/internal/application/updates"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
	"golang.org/x/term"
)

// closeTimeout bounds App.Close's joins when the caller passes no
// deadline.
const closeTimeout = 15 * time.Second

var Version = "dev"

type App struct {
	Server     *http.Server
	Settings   *config.Store
	Repository *sqlite.Repository
	Engine     io.Closer
	// Service owns the worker, engine, and repository lifetime. Close
	// routes through it so workers join before engine/repository close.
	Service *application.Service
	// Portal is the integration hub; Updates is the self-update
	// coordinator. Both run on the app's integration context, which Close
	// cancels and joins before the service shuts down.
	Portal  *portal.Hub
	Updates *updates.Manager

	ListenAddress string
	// BeforeHandoffExit runs immediately before the update coordinator
	// exits the process for a helper relaunch. The GUI releases its
	// single-instance lock here so the relaunched application can acquire
	// it.
	BeforeHandoffExit func()

	runCtx    context.Context
	runCancel context.CancelFunc
	runDone   chan struct{}
	// onStartupUpdate is a test seam invoked at the readiness moment
	// instead of the coordinator trigger.
	onStartupUpdate func()
}

// NewAt assembles the application against an explicit settings file path;
// the data-dir layer resolves that path before calling (env
// TORRENT_TV_SETTINGS_PATH wins only because callers pass
// env-if-set-else-resolved). Both the headless server and the GUI
// supervisor build through this constructor so there is exactly one
// settings store per process.
func NewAt(settingsPath string, log *slog.Logger) (*App, error) {
	settings, err := config.LoadAt(settingsPath)
	if err != nil {
		return nil, err
	}
	return assemble(settings, log)
}

// assemble builds the application around an already-loaded settings store:
// onboarding, media-tool discovery, and every adapter wiring step.
func assemble(settings *config.Store, log *slog.Logger) (*App, error) {
	// First-run onboarding: when a required setting is neither in the
	// settings file nor the environment, ask for it before the engine is
	// built, so an unwritable default download root becomes a question
	// instead of a crash loop. Headless runs cannot answer and fall back
	// to the defaults with a warning.
	if missing := settings.MissingRequired(); len(missing) > 0 {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			console := config.Console{
				In:  os.Stdin,
				Out: os.Stdout,
				Secret: func() ([]byte, error) {
					return term.ReadPassword(int(os.Stdin.Fd()))
				},
			}
			if err := config.PromptRequired(settings, console, true); err != nil {
				return nil, err
			}
		} else {
			log.Warn("required settings missing; continuing with defaults", "settings", strings.Join(missing, ", "))
		}
	}
	// Media tools: discover ffprobe/ffmpeg on PATH when the configured
	// paths do not exist, persisting what is found. A missing tool only
	// degrades subtitle probing and audio fallback at runtime, so warn
	// instead of failing startup.
	unfound, err := settings.ResolveMediaTools()
	if err != nil {
		return nil, err
	}
	if len(unfound) > 0 {
		log.Warn("media tools not found; subtitle probing and audio fallback are unavailable",
			"tools", strings.Join(unfound, ", "),
			"hint", "install ffmpeg (brew install ffmpeg, apt install ffmpeg) or set the paths in Settings")
	}
	current := settings.Get()
	repo, err := sqlite.Open(current.DatabasePath)
	if err != nil {
		return nil, err
	}
	fl := filelist.New(func() (string, string, string) {
		v := settings.Get()
		return v.FileListURL, v.FileListUsername, v.FileListPasskey
	})
	var engine application.TorrentEngine
	var engineCloser io.Closer
	routePrefix := "qb:"
	switch current.DownloadEngine {
	case "", "native":
		nt, err := nativetorrent.New(nativetorrent.Config{
			DataDir:     current.DownloadRoot,
			SessionDir:  current.TorrentSessionDir,
			PeerPort:    current.TorrentPeerPort,
			Readahead:   current.ReadAheadBytes,
			StartWindow: current.InitialBufferBytes,
		})
		if err != nil {
			return nil, fmt.Errorf("native torrent engine: %w", err)
		}
		engine, engineCloser, routePrefix = nt, nt, "native:"
	case "qbittorrent":
		engine = qbittorrent.New(func() (string, string, string) {
			v := settings.Get()
			return v.QBittorrentURL, v.QBittorrentUsername, v.QBittorrentPassword
		})
	default:
		return nil, fmt.Errorf("unknown download engine %q", current.DownloadEngine)
	}
	service := application.NewService(fl, engine, repo, settings, subtitles.NewSubDL(settings))
	service.SetMetadataProvider(tmdb.New(func() string { return settings.Get().TMDBAPIKey }))
	service.SetMediaProbe(mediaprobe.New(settings))
	service.SetEngineRoutePrefix(routePrefix)
	service.StartScheduler()

	// Integration wiring: the hub and the update coordinator are
	// constructed completely before anything binds to them, and they
	// publish through the service's journal-plus-fan-out event path. The
	// update coordinator's stop hook drains HTTP and joins the service —
	// the exact ordering the handoff barrier relies on.
	app := &App{
		Settings:      settings,
		Repository:    repo,
		Engine:        engineCloser,
		Service:       service,
		ListenAddress: current.ListenAddress,
	}
	sink := func(kind string, payload any) { service.PublishEvent(kind, payload) }
	hub := portal.NewHub(
		portalclient.New(portalHTTPClient()),
		func() string { return settings.Get().PortalAPIKey },
		time.Now,
		jitterInterval,
		sink,
	)
	noticeHint := func(ctx context.Context) (string, bool, error) {
		notice, ok, err := hub.RefreshNotice(ctx)
		if err != nil || !ok {
			return "", ok, err
		}
		return notice.Version, true, nil
	}
	stopServing := func(ctx context.Context) error {
		drainErr := app.Server.Shutdown(ctx)
		closeErr := service.Close(ctx)
		if drainErr != nil {
			return drainErr
		}
		return closeErr
	}
	app.Updates = newUpdateManager(updateCoordinatorOptions{
		log:        log,
		notice:     noticeHint,
		stop:       stopServing,
		sink:       sink,
		beforeExit: func() { app.BeforeHandoffExit() },
	})
	app.Portal = hub

	// The integration loops run on the app's context: Close cancels and
	// joins them before the service shuts down, so no journal write can
	// race the repository close.
	runCtx, runCancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	var runGroup sync.WaitGroup
	runGroup.Add(2)
	go func() { defer runGroup.Done(); _ = hub.Run(runCtx) }()
	go func() { defer runGroup.Done(); _ = app.Updates.Run(runCtx) }()
	go func() { runGroup.Wait(); close(runDone) }()
	app.runCtx, app.runCancel, app.runDone = runCtx, runCancel, runDone

	handler := httpapi.New(
		service, settings, log, Version,
		httpapi.WithPortal(hub),
		httpapi.WithUpdates(app.Updates),
	)
	app.Server = &http.Server{Addr: current.ListenAddress, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	return app, nil
}

// ListenAndServe serves until Shutdown. The server's BaseContext hook
// runs exactly once the live listener is accepting — the readiness moment
// that schedules the once-per-app startup update.
func (a *App) ListenAndServe() error {
	base := a.Server.BaseContext
	a.Server.BaseContext = func(ln net.Listener) context.Context {
		a.triggerStartupUpdate()
		if base != nil {
			return base(ln)
		}
		return context.Background()
	}
	return a.Server.ListenAndServe()
}

// triggerStartupUpdate schedules the once-per-app post-readiness update
// sequence: pending-operation recovery with the health acknowledgement,
// then one bounded, fail-open automatic apply. It never blocks serving.
func (a *App) triggerStartupUpdate() {
	if a.onStartupUpdate != nil {
		a.onStartupUpdate()
		return
	}
	if a.Updates == nil || a.runCtx == nil {
		return
	}
	a.Updates.StartupApply(context.WithoutCancel(a.runCtx))
}

// Close stops the integration loops, then joins workers and closes the
// engine and repository through Service.Close — the S3 ordering. A bounded
// default applies when ctx carries no deadline; a join timeout aborts the
// close — and with it any pending update handoff — with an error rather
// than closing the database under active writers.
func (a *App) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, closeTimeout)
		defer cancel()
	}
	if a.runCancel != nil {
		a.runCancel()
		select {
		case <-a.runDone:
		case <-ctx.Done():
			return fmt.Errorf("integration loops did not stop: %w", ctx.Err())
		}
	}
	if a.Service != nil {
		return a.Service.Close(ctx)
	}
	if a.Engine != nil {
		_ = a.Engine.Close()
	}
	if a.Repository != nil {
		return a.Repository.Close()
	}
	return nil
}

// updateCoordinatorOptions carries the collaborators assemble (or the
// --update command) injects into the self-update coordinator.
type updateCoordinatorOptions struct {
	log        *slog.Logger
	notice     updates.NoticeHint
	stop       func(context.Context) error
	sink       updates.Sink
	beforeExit func()
}

// newUpdateManager builds the self-update coordinator for this
// installation: the ldflags-injected identity, the flavor detected from
// the actual install shape, the fixed repository's release feed, and the
// invocation a helper relaunch must resume (minus update markers).
func newUpdateManager(opts updateCoordinatorOptions) *updates.Manager {
	executable, exeErr := os.Executable()
	if exeErr != nil && opts.log != nil {
		opts.log.Warn("self-update unavailable: executable path not resolvable", "error", exeErr.Error())
	}
	identity := updates.Identity{
		Version: Version,
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Flavor:  updates.DetectFlavor(runtime.GOOS, runtime.GOARCH, bundleInstall(executable, exeErr)),
	}
	if opts.sink == nil {
		opts.sink = func(string, any) {}
	}
	return updates.NewManager(updates.ManagerDeps{
		Identity:     identity,
		Resolver:     updates.NewResolver(identity, releaseFeed{}),
		Notice:       opts.notice,
		Assets:       fetchReleaseAsset,
		Sink:         opts.sink,
		Jitter:       jitterInterval,
		InstallDir:   filepath.Dir(executable),
		Executable:   executable,
		StopServing:  opts.stop,
		BeforeExit:   opts.beforeExit,
		RelaunchArgs: relaunchArgs(),
	})
}

// NewUpdateCoordinator builds the self-update coordinator without a
// running application: the `serve --update` command uses it for the
// blocking check and install before normal serving. The announcement hint
// talks to the portal update feed directly, and there is no serving
// surface to drain on this path.
func NewUpdateCoordinator(log *slog.Logger) *updates.Manager {
	notice := func(ctx context.Context) (string, bool, error) {
		notice, err := portalclient.New(portalHTTPClient()).Notice(ctx)
		if err != nil {
			if errors.Is(err, portal.ErrNoticeAbsent) {
				return "", false, nil
			}
			return "", false, err
		}
		return notice.Version, true, nil
	}
	// The standalone --update coordinator must journal its operation
	// failures somewhere a human can read them; a no-op sink turned every
	// staging failure into a silent fallback to the running version.
	return newUpdateManager(updateCoordinatorOptions{
		log:    log,
		notice: notice,
		sink: func(event string, payload any) {
			log.Info("updates event", "event", event, "payload", payload)
		},
	})
}

// bundleInstall reports whether the running executable lives inside a
// macOS .app bundle: the actual install shape selects the bundle flavor.
func bundleInstall(executable string, err error) bool {
	return err == nil && runtime.GOOS == "darwin" && strings.Contains(executable, ".app/Contents/")
}

// relaunchArgs returns this process invocation with internal update
// markers stripped: a helper-relaunched installation resumes the original
// command line — data-dir identity included — minus --update, so the CLI
// step and the coordinator can never restart through both paths.
func relaunchArgs() []string {
	args := make([]string, 0, len(os.Args))
	for _, arg := range os.Args[1:] {
		if arg == "--update" || strings.HasPrefix(arg, "--update=") {
			continue
		}
		args = append(args, arg)
	}
	return args
}

// portalHTTPClient is the bounded transport for the fixed upstream
// integration host; requests carry their own deadlines.
func portalHTTPClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

// releaseFeed implements updates.MetadataSource against the fixed
// repository's GitHub release feed. Requests never carry integration
// credentials, and every body is read with a bounded limit.
type releaseFeed struct{}

const (
	releaseFeedURL       = "https://api.github.com/repos/mihaiflorentin88/torrent-tv/releases/latest"
	releaseFeedReadLimit = 4 << 20
	manifestReadLimit    = 1 << 20
)

// releaseFeedToken returns the optional GitHub API token for the release
// feed: unauthenticated api.github.com requests share a 60-per-hour budget
// per egress IP that hosted boxes exhaust quickly. Precedence: the
// app-specific variable, then the two GitHub convention names.
func releaseFeedToken() string {
	for _, key := range []string{"TORRENT_TV_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(key)); token != "" {
			return token
		}
	}
	return ""
}

func newReleaseFeedRequest(ctx context.Context) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseFeedURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	if token := releaseFeedToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request, nil
}

func (releaseFeed) LatestRelease(ctx context.Context) (updates.Release, error) {
	request, err := newReleaseFeedRequest(ctx)
	if err != nil {
		return updates.Release{}, err
	}
	response, err := portalHTTPClient().Do(request)
	if err != nil {
		return updates.Release{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return updates.Release{}, fmt.Errorf("release feed responded %d", response.StatusCode)
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, releaseFeedReadLimit)).Decode(&body); err != nil {
		return updates.Release{}, fmt.Errorf("decode release feed: %w", err)
	}
	release := updates.Release{
		Tag:         body.TagName,
		URL:         body.HTMLURL,
		Notes:       body.Body,
		Draft:       body.Draft,
		Prerelease:  body.Prerelease,
		PublishedAt: body.PublishedAt,
	}
	for _, asset := range body.Assets {
		release.Assets = append(release.Assets, updates.Asset{Name: asset.Name, URL: asset.BrowserDownloadURL})
	}
	return release, nil
}

func (releaseFeed) ChecksumManifest(ctx context.Context, manifestURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return "", err
	}
	response, err := portalHTTPClient().Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum manifest responded %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, manifestReadLimit+1))
	if err != nil {
		return "", fmt.Errorf("read checksum manifest: %w", err)
	}
	if len(body) > manifestReadLimit {
		return "", fmt.Errorf("checksum manifest exceeds %d bytes", manifestReadLimit)
	}
	return string(body), nil
}

// fetchReleaseAsset streams the resolved release asset for staging. The
// client allows redirects only inside the repository release
// infrastructure (GitHub release pages redirect downloads to their
// object store), and never forwards credentials — none are ever attached.
func fetchReleaseAsset(ctx context.Context, sel updates.Selection) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sel.AssetURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: 15 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return errors.New("too many release download redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("insecure release redirect to %s", req.URL.Host)
			}
			switch req.URL.Hostname() {
			case "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com":
				return nil
			default:
				return fmt.Errorf("release redirect outside repository infrastructure: %s", req.URL.Hostname())
			}
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("release asset %q responded %d", sel.AssetName, response.StatusCode)
	}
	return response.Body, nil
}

// jitterInterval returns a bounded, always-positive offset in [5%, 15%)
// of the base interval so installations never check or refresh in
// lockstep.
func jitterInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	span := d / 10
	if span <= 0 {
		return 0
	}
	return span/2 + time.Duration(rand.Int64N(int64(span)))
}

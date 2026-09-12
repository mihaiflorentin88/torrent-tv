package portal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeClient is an in-package Client double: every endpoint fails or serves
// independently so the tests can exercise per-surface failure and recovery.
type fakeClient struct {
	mu                sync.Mutex
	settingsCalls     int
	linksCalls        int
	settings          PublicSettings
	settingsErr       error
	links             []Link
	linksErr          error
	notice            Notice
	noticeErr         error
	noticeCalls       int
	promotions        []Promotion
	promotionsErr     error
	promotionCalls    int
	available         bool
	availableErr      error
	availabilityCalls int
	status            AccountStatus
	statusErr         error
	statusKeys        []string
	loginErr          error
	loginCalls        int
	registerErr       error
	meErr             error
	clickDest         string
	clickErr          error

	gateAccount chan struct{} // when non-nil, AccountStatus blocks until closed
	gateLinks   chan struct{} // when non-nil, Links blocks until closed
}

func (f *fakeClient) Settings(context.Context) (PublicSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settingsCalls++
	return f.settings, f.settingsErr
}

func (f *fakeClient) Links(context.Context) ([]Link, error) {
	f.mu.Lock()
	gate := f.gateLinks
	f.linksCalls++
	links, err := f.links, f.linksErr
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return links, err
}

func (f *fakeClient) Notice(context.Context) (Notice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noticeCalls++
	return f.notice, f.noticeErr
}

func (f *fakeClient) Promotions(_ context.Context, _ int) ([]Promotion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promotionCalls++
	return f.promotions, f.promotionsErr
}

func (f *fakeClient) PromotionAvailability(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.availabilityCalls++
	return f.available, f.availableErr
}

func (f *fakeClient) AccountStatus(_ context.Context, apiKey string) (AccountStatus, error) {
	f.mu.Lock()
	gate := f.gateAccount
	f.statusKeys = append(f.statusKeys, apiKey)
	status, err := f.status, f.statusErr
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return status, err
}

func (f *fakeClient) Login(context.Context, string, string) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginCalls++
	return Session{Token: "token"}, f.loginErr
}

func (f *fakeClient) Register(context.Context, string, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registerErr
}

func (f *fakeClient) Me(context.Context, string) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return User{ID: 7}, f.meErr
}

func (f *fakeClient) Click(context.Context, string, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clickDest, f.clickErr
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type recordedEvent struct {
	kind    string
	payload []byte
}

type recordingSink struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (r *recordingSink) sink(kind string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedEvent{kind: kind, payload: b})
}

func (r *recordingSink) recorded() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedEvent(nil), r.events...)
}

// keyHolder mimics the live settings reader: the hub sees whatever the
// closure returns at call time.
type keyHolder struct {
	mu  sync.Mutex
	key string
}

func (k *keyHolder) read() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.key
}

func (k *keyHolder) set(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.key = key
}

var (
	healthyLinks = []Link{{ID: 1, Title: "Other project", URL: "https://example.invalid/project"}}
	healthyAds   = []Promotion{{ID: "ad-1", Provider: "house", Title: "Support", ScreenTime: 10}}
)

func healthyFake() *fakeClient {
	return &fakeClient{
		settings:   PublicSettings{AccountsEnabled: true, AdsEnabled: true},
		links:      healthyLinks,
		promotions: healthyAds,
		available:  true,
	}
}

func newTestHub(client *fakeClient, keys *keyHolder, sink *recordingSink) *Hub {
	return NewHub(client, keys.read, newFakeClock().Now, nil, sink.sink)
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHubStartsInactive(t *testing.T) {
	hub := newTestHub(healthyFake(), &keyHolder{}, &recordingSink{})
	snapshot := hub.Snapshot()
	if snapshot.AccountsEnabled || snapshot.AdsEnabled || snapshot.Donor {
		t.Fatalf("initial snapshot must be inactive: %#v", snapshot)
	}
	if snapshot.Links == nil || len(snapshot.Links) != 0 {
		t.Fatalf("inactive links must marshal as an empty array, got %#v", snapshot.Links)
	}
	if promotions := hub.Promotions(); promotions == nil || len(promotions) != 0 {
		t.Fatalf("initial promotions must be an empty array, got %#v", promotions)
	}
	if _, ok := hub.Notice(); ok {
		t.Fatal("no notice has been fetched yet, presence must be false")
	}
}

func TestHubRefreshAppliesHealthyStateAndPublishesSnapshot(t *testing.T) {
	client := healthyFake()
	sink := &recordingSink{}
	hub := newTestHub(client, &keyHolder{}, sink)
	client.mu.Lock()
	client.status = AccountStatus{Donor: false}
	client.mu.Unlock()

	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := hub.Snapshot()
	if !snapshot.AccountsEnabled || !snapshot.AdsEnabled || snapshot.Donor {
		t.Fatalf("a healthy non-donor refresh must enable surfaces without donor state: %#v", snapshot)
	}
	if len(snapshot.Links) != 1 || snapshot.Links[0].ID != 1 {
		t.Fatalf("links were not applied: %#v", snapshot.Links)
	}
	events := sink.recorded()
	if len(events) != 1 {
		t.Fatalf("expected exactly one portal.state event, got %d", len(events))
	}
	if events[0].kind != "portal.state" {
		t.Fatalf("unexpected event kind %q", events[0].kind)
	}
	var carried Snapshot
	if err := json.Unmarshal(events[0].payload, &carried); err != nil {
		t.Fatalf("portal.state payload must carry a JSON Snapshot: %v", err)
	}
	if !carried.AccountsEnabled || !carried.AdsEnabled || carried.Donor {
		t.Fatalf("published snapshot lost state: %#v", carried)
	}
}

func TestHubPublicSettingsFailureClearsAccountsAndPromotionsKeepsLinks(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.settingsErr = errors.New("upstream 503")
	client.mu.Unlock()
	if err := hub.Refresh(context.Background()); err == nil {
		t.Fatal("settings failure must surface as a refresh error")
	}
	snapshot := hub.Snapshot()
	if snapshot.AccountsEnabled || snapshot.AdsEnabled || snapshot.Donor {
		t.Fatalf("public-settings failure must clear accounts and promotions: %#v", snapshot)
	}
	if len(snapshot.Links) != 1 {
		t.Fatalf("public-settings failure must leave links alone: %#v", snapshot.Links)
	}
	if promotions := hub.Promotions(); len(promotions) != 0 {
		t.Fatalf("promotions must be cleared on public-settings failure: %#v", promotions)
	}
}

func TestHubLinksFailureRemovesLinksAndRecoveryRestoresThem(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.linksErr = errors.New("upstream 503")
	client.mu.Unlock()
	if err := hub.Refresh(context.Background()); err == nil {
		t.Fatal("links failure must surface as a refresh error")
	}
	snapshot := hub.Snapshot()
	if !snapshot.AccountsEnabled || !snapshot.AdsEnabled {
		t.Fatalf("links failure must leave accounts and promotions alone: %#v", snapshot)
	}
	if len(snapshot.Links) != 0 {
		t.Fatalf("a failed links fetch must remove the links surface: %#v", snapshot.Links)
	}
	client.mu.Lock()
	client.linksErr = nil
	client.mu.Unlock()
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot := hub.Snapshot(); len(snapshot.Links) != 1 || snapshot.Links[0].ID != 1 {
		t.Fatalf("a later successful probe must restore links: %#v", snapshot.Links)
	}
}

func TestHubAccountOutageHidesControlsUntilHealthProbeRecovers(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.statusErr = fmt.Errorf("outage: %w", ErrUnavailable)
	client.mu.Unlock()
	if err := hub.Refresh(context.Background()); err == nil {
		t.Fatal("account outage must surface as a refresh error")
	}
	if snapshot := hub.Snapshot(); snapshot.AccountsEnabled {
		t.Fatalf("account transport outage must hide account controls: %#v", snapshot)
	}
	if snapshot := hub.Snapshot(); !snapshot.AdsEnabled || len(snapshot.Links) != 1 {
		t.Fatalf("account outage must leave unrelated surfaces alone: %#v", snapshot)
	}
	client.mu.Lock()
	client.statusErr = nil
	client.mu.Unlock()
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot := hub.Snapshot(); !snapshot.AccountsEnabled {
		t.Fatalf("a later successful health probe must restore the gate: %#v", snapshot)
	}
}

func TestHubLoginCredentialRejectionIsNotAnOutage(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.loginErr = fmt.Errorf("rejected: %w", ErrCredentials)
	client.mu.Unlock()
	if _, err := hub.Login(context.Background(), "user@example.invalid", "pw"); err == nil {
		t.Fatal("credential rejection must surface to the caller")
	}
	if snapshot := hub.Snapshot(); !snapshot.AccountsEnabled {
		t.Fatalf("a 401 login must not hide account controls: %#v", snapshot)
	}
	client.mu.Lock()
	client.loginErr = fmt.Errorf("outage: %w", ErrUnavailable)
	client.mu.Unlock()
	if _, err := hub.Login(context.Background(), "user@example.invalid", "pw"); err == nil {
		t.Fatal("transport outage must surface to the caller")
	}
	if snapshot := hub.Snapshot(); snapshot.AccountsEnabled {
		t.Fatalf("an account transport outage must hide controls: %#v", snapshot)
	}
}

func TestHubGatesAccountOperationsUntilRefreshEnablesThem(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	if _, err := hub.Login(context.Background(), "user@example.invalid", "pw"); !errors.Is(err, ErrAccountsUnavailable) {
		t.Fatalf("account operations before a refresh must be gated, got %v", err)
	}
	client.mu.Lock()
	loginCalls := client.loginCalls
	client.mu.Unlock()
	if loginCalls != 0 {
		t.Fatal("gated account operations must not reach upstream")
	}
	if _, err := hub.Me(context.Background(), "token"); !errors.Is(err, ErrAccountsUnavailable) {
		t.Fatalf("me is an account operation and must be gated, got %v", err)
	}
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Login(context.Background(), "user@example.invalid", "pw"); err != nil {
		t.Fatalf("enabled accounts must allow login: %v", err)
	}
}

func TestHubGatesClicksWhilePromotionsAreHidden(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	client.mu.Lock()
	client.clickDest = "https://partner.example.invalid/landing"
	client.mu.Unlock()
	if _, err := hub.Click(context.Background(), "house", "ad-1"); !errors.Is(err, ErrPromotionsUnavailable) {
		t.Fatalf("clicks before a refresh must be gated, got %v", err)
	}
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	dest, err := hub.Click(context.Background(), "house", "ad-1")
	if err != nil || dest != "https://partner.example.invalid/landing" {
		t.Fatalf("live promotions must allow clicks: %q %v", dest, err)
	}
}

func TestHubDonorExpiryScheduledAtActualExpiryTime(t *testing.T) {
	client := healthyFake()
	sink := &recordingSink{}
	clock := newFakeClock()
	hub := NewHub(client, (&keyHolder{}).read, clock.Now, nil, sink.sink)
	until := clock.Now().Add(80 * time.Millisecond)
	client.mu.Lock()
	client.status = AccountStatus{Donor: true, DonorUntil: &until}
	client.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- hub.Run(ctx) }()
	waitFor(t, time.Second, func() bool { return hub.Snapshot().Donor }, "donor state from the initial refresh")

	clock.Advance(200 * time.Millisecond) // donorUntil is now in the past
	waitFor(t, 2*time.Second, func() bool { return !hub.Snapshot().Donor }, "donor expiry at the actual expiry time")
	// The snapshot flip and the sink delivery are two steps; wait for the
	// republish instead of racing it.
	waitFor(t, 2*time.Second, func() bool {
		recorded := sink.recorded()
		if len(recorded) == 0 || recorded[len(recorded)-1].kind != "portal.state" {
			return false
		}
		var published Snapshot
		return json.Unmarshal(recorded[len(recorded)-1].payload, &published) == nil && !published.Donor
	}, "the expiry republish to carry the expired donor state")
	events := sink.recorded()
	var carried Snapshot
	if err := json.Unmarshal(events[len(events)-1].payload, &carried); err != nil {
		t.Fatal(err)
	}
	if carried.Donor {
		t.Fatalf("published snapshot must carry the expired donor state: %#v", carried)
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run must return the cancellation: %v", err)
	}
}

func TestHubDonorAlreadyExpiredAppliesInactiveImmediately(t *testing.T) {
	client := healthyFake()
	clock := newFakeClock()
	hub := NewHub(client, (&keyHolder{}).read, clock.Now, nil, (&recordingSink{}).sink)
	past := clock.Now().Add(-time.Hour)
	client.mu.Lock()
	client.status = AccountStatus{Donor: true, DonorUntil: &past}
	client.mu.Unlock()
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot := hub.Snapshot(); snapshot.Donor {
		t.Fatalf("an already-expired donor status must not stay active: %#v", snapshot)
	}
}

func TestHubKeyReplacementDuringInFlightRequestCannotResurrectDonor(t *testing.T) {
	client := healthyFake()
	keys := &keyHolder{key: "k1"}
	hub := NewHub(client, keys.read, newFakeClock().Now, nil, (&recordingSink{}).sink)
	until := newFakeClock().Now().Add(time.Hour)
	client.mu.Lock()
	client.status = AccountStatus{Donor: true, DonorUntil: &until}
	client.gateAccount = make(chan struct{})
	client.mu.Unlock()

	refreshErr := make(chan error, 1)
	go func() { refreshErr <- hub.Refresh(context.Background()) }()
	waitFor(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.statusKeys) == 1
	}, "the in-flight account request")

	keys.set("k2")           // the supporter key was replaced mid-flight
	if !hub.noteSettings() { // the poll notices the change and bumps the generation
		t.Fatal("a key change must be reported")
	}
	client.mu.Lock()
	close(client.gateAccount)
	client.mu.Unlock()

	if err := <-refreshErr; !errors.Is(err, errSuperseded) {
		t.Fatalf("the in-flight refresh must be discarded as stale, got %v", err)
	}
	if snapshot := hub.Snapshot(); snapshot.Donor {
		t.Fatalf("a stale response for the replaced key must not resurrect donor state: %#v", snapshot)
	}
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	seenKeys := append([]string(nil), client.statusKeys...)
	client.mu.Unlock()
	if len(seenKeys) != 2 || seenKeys[0] != "k1" || seenKeys[1] != "k2" {
		t.Fatalf("the follow-up refresh must use the replaced key: %v", seenKeys)
	}
	if snapshot := hub.Snapshot(); !snapshot.Donor {
		t.Fatalf("the fresh key's donor state must apply: %#v", snapshot)
	}
}

func TestHubSnapshotSlicesNeverAliasInternalState(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := hub.Snapshot()
	snapshot.Links[0].Title = "mutated"
	promotions := hub.Promotions()
	promotions[0].Title = "mutated"
	if fresh := hub.Snapshot(); fresh.Links[0].Title != "Other project" {
		t.Fatalf("snapshot links alias internal state: %#v", fresh.Links)
	}
	if fresh := hub.Promotions(); fresh[0].Title != "Support" {
		t.Fatalf("promotions alias internal state: %#v", fresh)
	}
}

func TestHubCancellationDuringRefreshLeavesStateUntouched(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	client.mu.Lock()
	client.gateLinks = make(chan struct{})
	client.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	refreshErr := make(chan error, 1)
	go func() { refreshErr <- hub.Refresh(ctx) }()
	waitFor(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.linksCalls == 1
	}, "the blocked links request")
	cancel()
	client.mu.Lock()
	close(client.gateLinks)
	client.mu.Unlock()
	if err := <-refreshErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh must return the cancellation, got %v", err)
	}
	snapshot := hub.Snapshot()
	if snapshot.AccountsEnabled || snapshot.AdsEnabled || snapshot.Donor || len(snapshot.Links) != 0 {
		t.Fatalf("a cancelled refresh must not change state: %#v", snapshot)
	}
}

func TestHubRunShutdownBeforeAnyRefreshBegan(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- hub.Run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run must return the cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a cancelled context must not deadlock Run before the first refresh")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.settingsCalls != 0 {
		t.Fatalf("shutdown before the first refresh must not touch upstream, got %d calls", client.settingsCalls)
	}
}

func TestHubSettingsChangeTriggersRefreshWithoutHourlyWait(t *testing.T) {
	client := healthyFake()
	keys := &keyHolder{key: "k1"}
	hub := NewHub(client, keys.read, newFakeClock().Now, nil, (&recordingSink{}).sink)
	hub.pollInterval = 5 * time.Millisecond
	hub.refreshInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- hub.Run(ctx) }()
	waitFor(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.settingsCalls >= 1
	}, "the initial refresh")
	keys.set("k2")
	waitFor(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.settingsCalls >= 2
	}, "a refresh triggered by the settings change")
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run must return the cancellation: %v", err)
	}
}

func TestHubRunRejectsConcurrentRuns(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	hub.refreshInterval = time.Hour
	hub.pollInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- hub.Run(ctx) }()
	defer cancel()
	waitFor(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.settingsCalls >= 1
	}, "the first Run to take ownership")
	if err := hub.Run(ctx); err == nil {
		t.Fatal("a second concurrent Run must be rejected")
	}
}

func TestHubNoticePresenceIsExplicitAndClearedOnFailedFetch(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	notice := Notice{Version: "v1.2.3", Notes: "see the release page"}
	client.mu.Lock()
	client.notice = notice
	client.mu.Unlock()
	got, ok, err := hub.RefreshNotice(context.Background())
	if err != nil || !ok || got.Version != "v1.2.3" {
		t.Fatalf("expected the published notice, got %#v %v %v", got, ok, err)
	}
	if cached, ok := hub.Notice(); !ok || cached.Version != "v1.2.3" {
		t.Fatalf("cached notice presence must be explicit, got %#v %v", cached, ok)
	}

	client.mu.Lock()
	client.noticeErr = ErrNoticeAbsent
	client.mu.Unlock()
	if _, ok, err := hub.RefreshNotice(context.Background()); err != nil || ok {
		t.Fatalf("an absent notice is not a failure, got %v %v", ok, err)
	}
	if _, ok := hub.Notice(); ok {
		t.Fatal("an absent notice must clear the cached presence")
	}

	client.mu.Lock()
	client.noticeErr = fmt.Errorf("outage: %w", ErrUnavailable)
	client.mu.Unlock()
	if _, ok, err := hub.RefreshNotice(context.Background()); err == nil || ok {
		t.Fatalf("a failed fetch must surface the error without presence, got %v %v", ok, err)
	}
	if _, ok := hub.Notice(); ok {
		t.Fatal("a failed fetch must clear the cached notice")
	}
}

func TestHubNoticeReturnsBinariesCopies(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	client.mu.Lock()
	client.notice = Notice{Version: "v1.2.3", Binaries: []Binary{{Platform: "linux-amd64", DownloadURL: "https://example.invalid/a.tar.gz"}}}
	client.mu.Unlock()
	if _, ok, err := hub.RefreshNotice(context.Background()); err != nil || !ok {
		t.Fatalf("expected the notice, got %v %v", ok, err)
	}
	got, ok := hub.Notice()
	if !ok || len(got.Binaries) != 1 {
		t.Fatalf("expected the cached binaries, got %#v %v", got, ok)
	}
	got.Binaries[0].Platform = "mutated"
	fresh, _ := hub.Notice()
	if fresh.Binaries[0].Platform != "linux-amd64" {
		t.Fatalf("returned notice aliases cached state: %#v", fresh.Binaries)
	}
}

func TestHubValidDonorSuppressesPromotionDeliveryUntilExpiry(t *testing.T) {
	client := healthyFake()
	clock := newFakeClock()
	hub := NewHub(client, (&keyHolder{}).read, clock.Now, nil, (&recordingSink{}).sink)
	until := clock.Now().Add(80 * time.Millisecond)
	client.mu.Lock()
	client.status = AccountStatus{Donor: true, DonorUntil: &until}
	client.mu.Unlock()

	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := hub.Snapshot()
	if !snapshot.Donor || snapshot.AdsEnabled {
		t.Fatalf("a valid donor must hide the promotion slot: %#v", snapshot)
	}
	if promotions := hub.Promotions(); len(promotions) != 0 {
		t.Fatalf("a donor must not keep cached promotions: %#v", promotions)
	}
	client.mu.Lock()
	deliveries, probes := client.promotionCalls, client.availabilityCalls
	client.mu.Unlock()
	if deliveries != 0 || probes != 0 {
		t.Fatalf("a donor must never trigger delivery or probes: deliveries=%d probes=%d", deliveries, probes)
	}

	clock.Advance(200 * time.Millisecond) // the donor expiry passes
	hub.expireDonor()
	if snapshot := hub.Snapshot(); snapshot.Donor {
		t.Fatal("donor status must expire at its actual time")
	}
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	deliveries, probes = client.promotionCalls, client.availabilityCalls
	client.mu.Unlock()
	if probes != 1 || deliveries != 1 {
		t.Fatalf("donor expiry must restore the slot through the availability probe: deliveries=%d probes=%d", deliveries, probes)
	}
	if snapshot := hub.Snapshot(); !snapshot.AdsEnabled {
		t.Fatalf("the promotion slot must return after donor expiry: %#v", snapshot)
	}
}

func TestHubPromotionDeliveryFailureHidesSlotAndRecoversViaProbe(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.promotionsErr = errors.New("upstream 503")
	client.mu.Unlock()
	if err := hub.Refresh(context.Background()); err == nil {
		t.Fatal("delivery failure must surface as a refresh error")
	}
	if snapshot := hub.Snapshot(); snapshot.AdsEnabled {
		t.Fatalf("delivery failure must hide the promotion slot: %#v", snapshot)
	}
	if promotions := hub.Promotions(); len(promotions) != 0 {
		t.Fatalf("delivery failure must clear cached promotions: %#v", promotions)
	}
	client.mu.Lock()
	availabilityAfterFailure := client.availabilityCalls
	client.promotionsErr = nil
	client.mu.Unlock()
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	availabilityCalls := client.availabilityCalls
	promotionCalls := client.promotionCalls
	client.mu.Unlock()
	if availabilityCalls != availabilityAfterFailure+1 {
		t.Fatalf("recovery must go through the non-impression availability probe: %d -> %d", availabilityAfterFailure, availabilityCalls)
	}
	if snapshot := hub.Snapshot(); !snapshot.AdsEnabled {
		t.Fatalf("a successful probe and delivery must restore the slot: %#v", snapshot)
	}
	if promotions := hub.Promotions(); len(promotions) != 1 {
		t.Fatalf("recovered promotions must be cached: %#v", promotions)
	}
	_ = promotionCalls

	// While the slot is live, the hourly refresh delivers directly and must
	// not spend availability probes.
	before := hubProbeCount(client)
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := hubProbeCount(client); after != before {
		t.Fatalf("a live slot must not trigger availability probes: %d -> %d", before, after)
	}
}

func hubProbeCount(client *fakeClient) int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.availabilityCalls
}

func TestHubPromotionProbeRefusingCreativesKeepsSlotHidden(t *testing.T) {
	client := healthyFake()
	hub := newTestHub(client, &keyHolder{}, &recordingSink{})
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.promotionsErr = errors.New("upstream 503")
	client.mu.Unlock()
	_ = hub.Refresh(context.Background())
	client.mu.Lock()
	promotionsErr := client.promotionsErr
	promotionCalls := client.promotionCalls
	client.promotionsErr = nil
	client.available = false // the weights endpoint reports no creatives
	client.mu.Unlock()
	_ = promotionsErr
	if err := hub.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.promotionCalls != promotionCalls {
		t.Fatal("an absent creative pool must not trigger a delivery")
	}
	if snapshot := hub.Snapshot(); snapshot.AdsEnabled {
		t.Fatalf("the slot must stay hidden while no creative exists: %#v", snapshot)
	}
}

func TestHubRefreshIntervalIsOneHourPlusBoundedJitter(t *testing.T) {
	hub := NewHub(healthyFake(), (&keyHolder{}).read, newFakeClock().Now, nil, nil)
	if hub.refreshInterval != time.Hour {
		t.Fatalf("production refresh interval must be one hour, got %v", hub.refreshInterval)
	}
	if got := hub.nextInterval(); got != time.Hour {
		t.Fatalf("without jitter the interval stays one hour, got %v", got)
	}
	hub.jitter = func(time.Duration) time.Duration { return 5 * time.Minute }
	if got := hub.nextInterval(); got != 65*time.Minute {
		t.Fatalf("jitter must extend the base interval, got %v", got)
	}
	hub.jitter = func(time.Duration) time.Duration { return -2 * time.Hour }
	if got := hub.nextInterval(); got != time.Hour {
		t.Fatalf("a non-positive effective interval must fall back to the base, got %v", got)
	}
}

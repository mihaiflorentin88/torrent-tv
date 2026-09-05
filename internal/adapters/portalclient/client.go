// Package portalclient implements the portal.Client contract against the
// fixed upstream integration service. The base URL is a constant of this
// adapter only; composition injects the HTTP client, whose transport tests
// replace to intercept the fixed host. Requests carry a five-second
// deadline, responses are read with a bounded limit that rejects overflow,
// and failures are typed so credential rejection stays distinct from a
// transport outage. Errors never carry credentials or upstream bodies.
package portalclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/application/portal"
)

// baseURL is the fixed upstream address. It is deliberately not configurable.
const baseURL = "https://filelist-ads.ffxivbard.com"

// errNotFound marks an upstream 404: callers translate it per endpoint
// (an absent notice is not an outage; an empty promotion pool is not an
// error).
var errNotFound = errors.New("upstream resource not found")

const (
	requestDeadline = 5 * time.Second
	maxBodyBytes    = 4 << 20
)

var (
	// ErrCredentials marks rejected or missing credentials (401/403): a form
	// error, never a service outage.
	ErrCredentials = portal.ErrCredentials
	// ErrUnavailable marks transport outages and upstream 5xx responses.
	ErrUnavailable = portal.ErrUnavailable
	// ErrNoticeAbsent marks a 404 from the update feed: nothing published
	// yet, distinct from a failure to fetch.
	ErrNoticeAbsent = portal.ErrNoticeAbsent
)

// Compile-time proof the adapter satisfies the application contract.
var _ portal.Client = (*Client)(nil)

type Client struct {
	http *http.Client
}

// New builds the upstream adapter around an injected HTTP client.
func New(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{}
	}
	return &Client{http: client}
}

// do issues one bounded request to path with an optional bearer token and
// JSON payload, decodes a 2xx body into out, and maps non-2xx statuses onto
// typed errors. The response body is always drained and closed.
func (c *Client) do(ctx context.Context, method, path, token string, payload, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestDeadline)
	defer cancel()

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return statusError(resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := decodeBody(resp.Body, out); err != nil {
		return err
	}
	return nil
}

// statusError maps an upstream status onto a typed error without exposing
// the response body or any credential material.
func statusError(status int) error {
	switch {
	case status == http.StatusNotFound:
		return errNotFound
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrCredentials, status)
	case status >= 500:
		return fmt.Errorf("%w (HTTP %d)", ErrUnavailable, status)
	default:
		return fmt.Errorf("unexpected upstream status %d", status)
	}
}

// decodeBody reads at most maxBodyBytes and rejects larger responses before
// decoding.
func decodeBody(r io.Reader, out any) error {
	data, err := io.ReadAll(io.LimitReader(r, maxBodyBytes+1))
	if err != nil {
		return fmt.Errorf("%w: read response: %w", ErrUnavailable, err)
	}
	if len(data) > maxBodyBytes {
		return errors.New("upstream response exceeded size limit")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode upstream response: %w", err)
	}
	return nil
}

// upstream DTOs: snake-case shapes stay private to this adapter.

type settingsDTO struct {
	Ads       struct{ Enabled bool } `json:"ads"`
	Accounts  struct{ Enabled bool } `json:"accounts"`
	Supporter struct{ Enabled bool } `json:"supporter_plans"`
}

type linkDTO struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

type noticeDTO struct {
	Version     string      `json:"version"`
	Notes       string      `json:"notes"`
	ReleasedAt  time.Time   `json:"released_at"`
	DownloadURL string      `json:"download_url"`
	Binaries    []binaryDTO `json:"binaries"`
}

type binaryDTO struct {
	Platform    string `json:"platform"`
	DownloadURL string `json:"download_url"`
}

type adDTO struct {
	Provider   string `json:"provider"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	Image      string `json:"image"`
	ScreenTime int    `json:"screen_time"`
}

type weightDTO struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

type accountStatusDTO struct {
	Donor      bool    `json:"donor"`
	DonorUntil *string `json:"donor_until"`
}

type sessionDTO struct {
	Token     string     `json:"token"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type userDTO struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

func (c *Client) Settings(ctx context.Context) (portal.PublicSettings, error) {
	var dto settingsDTO
	if err := c.do(ctx, http.MethodGet, "/api/v1/settings", "", nil, &dto); err != nil {
		return portal.PublicSettings{}, err
	}
	return portal.PublicSettings{AccountsEnabled: dto.Accounts.Enabled, AdsEnabled: dto.Ads.Enabled}, nil
}

func (c *Client) Links(ctx context.Context) ([]portal.Link, error) {
	var dtos []linkDTO
	if err := c.do(ctx, http.MethodGet, "/api/v1/links", "", nil, &dtos); err != nil {
		return nil, err
	}
	links := make([]portal.Link, 0, len(dtos))
	for _, d := range dtos {
		links = append(links, portal.Link{ID: d.ID, Title: d.Title, URL: d.URL, Description: d.Description})
	}
	return links, nil
}

func (c *Client) Notice(ctx context.Context) (portal.Notice, error) {
	var dto noticeDTO
	if err := c.do(ctx, http.MethodGet, "/api/v1/updates", "", nil, &dto); err != nil {
		if errors.Is(err, errNotFound) {
			return portal.Notice{}, ErrNoticeAbsent
		}
		return portal.Notice{}, err
	}
	binaries := make([]portal.Binary, 0, len(dto.Binaries))
	for _, b := range dto.Binaries {
		binaries = append(binaries, portal.Binary{Platform: b.Platform, DownloadURL: b.DownloadURL})
	}
	return portal.Notice{
		Version:     dto.Version,
		Notes:       dto.Notes,
		ReleasedAt:  dto.ReleasedAt,
		DownloadURL: dto.DownloadURL,
		Binaries:    binaries,
	}, nil
}

func (c *Client) Promotions(ctx context.Context, count int) ([]portal.Promotion, error) {
	if count < 1 {
		count = 1
	}
	path := "/api/v1/ads?count=" + strconv.Itoa(count)
	var dtos []adDTO
	if err := c.do(ctx, http.MethodGet, path, "", nil, &dtos); err != nil {
		if errors.Is(err, errNotFound) {
			return []portal.Promotion{}, nil
		}
		return nil, err
	}
	promotions := make([]portal.Promotion, 0, len(dtos))
	for _, d := range dtos {
		promotions = append(promotions, portal.Promotion{
			ID:         d.ID,
			Provider:   d.Provider,
			Title:      d.Title,
			Text:       d.Text,
			Image:      d.Image,
			ScreenTime: d.ScreenTime,
		})
	}
	return promotions, nil
}

func (c *Client) PromotionAvailability(ctx context.Context) (bool, error) {
	var weights []weightDTO
	if err := c.do(ctx, http.MethodGet, "/api/v1/ads/weights", "", nil, &weights); err != nil {
		if errors.Is(err, errNotFound) {
			return false, nil
		}
		return false, err
	}
	return len(weights) > 0, nil
}

// Click resolves the upstream tracking redirect without following it and
// returns the destination only when it is an absolute HTTP(S) URL with a
// host. Path segments are validated before the request is constructed.
func (c *Client) Click(ctx context.Context, provider, id string) (string, error) {
	if !validSegment(provider) || !validSegment(id) {
		return "", errors.New("invalid promotion identifier")
	}
	path := "/api/v1/ads/" + provider + "/" + id + "/click"

	ctx, cancel := context.WithTimeout(ctx, requestDeadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}

	clicker := *c.http
	clicker.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := clicker.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 3 && resp.StatusCode/100 != 2 {
		return "", statusError(resp.StatusCode)
	}
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" {
		return "", errors.New("upstream click response carried no destination")
	}
	dest, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("invalid click destination: %w", err)
	}
	if (dest.Scheme != "http" && dest.Scheme != "https") || dest.Host == "" {
		return "", errors.New("invalid click destination")
	}
	return dest.String(), nil
}

// validSegment rejects empty segments and anything that could alter the
// request path: separators, whitespace, and control characters.
func validSegment(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	if strings.ContainsAny(s, "/?#") {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		return r <= ' ' || r == '\x7f'
	})
}

func (c *Client) AccountStatus(ctx context.Context, apiKey string) (portal.AccountStatus, error) {
	var dto accountStatusDTO
	if err := c.do(ctx, http.MethodGet, "/api/v1/account/status", apiKey, nil, &dto); err != nil {
		return portal.AccountStatus{}, err
	}
	status := portal.AccountStatus{Donor: dto.Donor}
	if dto.DonorUntil != nil && *dto.DonorUntil != "" {
		until, err := time.Parse(time.RFC3339, *dto.DonorUntil)
		if err != nil {
			return portal.AccountStatus{}, fmt.Errorf("decode donor expiry: %w", err)
		}
		status.DonorUntil = &until
	}
	return status, nil
}

func (c *Client) Login(ctx context.Context, email, password string) (portal.Session, error) {
	var dto sessionDTO
	err := c.do(ctx, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": email, "password": password,
	}, &dto)
	if err != nil {
		return portal.Session{}, err
	}
	session := portal.Session{Token: dto.Token}
	if dto.ExpiresAt != nil {
		session.ExpiresAt = *dto.ExpiresAt
	}
	return session, nil
}

func (c *Client) Register(ctx context.Context, email, password, displayName string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": email, "password": password, "display_name": displayName,
	}, nil)
}

func (c *Client) Me(ctx context.Context, token string) (portal.User, error) {
	var dto userDTO
	if err := c.do(ctx, http.MethodGet, "/api/v1/auth/me", token, nil, &dto); err != nil {
		return portal.User{}, err
	}
	return portal.User{ID: dto.ID, Email: dto.Email, DisplayName: dto.DisplayName, Role: dto.Role}, nil
}

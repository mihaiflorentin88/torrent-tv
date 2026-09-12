// Package portal is the application contract for portal integration: the
// neutral local types and the Client boundary the composition wires to the
// upstream HTTP adapter. Upstream snake-case DTOs stay private to the
// adapter.
package portal

import (
	"context"
	"time"
)

type Link struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

type Snapshot struct {
	AccountsEnabled bool   `json:"accountsEnabled"`
	AdsEnabled      bool   `json:"adsEnabled"`
	Donor           bool   `json:"donor"`
	Links           []Link `json:"links"`
}

type Promotion struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	Image      string `json:"image"`
	Link       string `json:"link"`
	ScreenTime int    `json:"screenTime"`
}

type Binary struct {
	Platform    string `json:"platform"`
	DownloadURL string `json:"download_url"`
}

type Notice struct {
	Version     string    `json:"version"`
	Notes       string    `json:"notes"`
	ReleasedAt  time.Time `json:"released_at"`
	DownloadURL string    `json:"download_url"`
	Binaries    []Binary  `json:"binaries"`
}

type Session struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type User struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type AccountStatus struct {
	Donor      bool
	DonorUntil *time.Time
}

type PublicSettings struct {
	AccountsEnabled bool
	AdsEnabled      bool
}

type Client interface {
	Settings(context.Context) (PublicSettings, error)
	Links(context.Context) ([]Link, error)
	Notice(context.Context) (Notice, error)
	Promotions(context.Context, int) ([]Promotion, error)
	PromotionAvailability(context.Context) (bool, error)
	Click(context.Context, string, string) (string, error)
	AccountStatus(context.Context, string) (AccountStatus, error)
	Login(context.Context, string, string) (Session, error)
	Register(context.Context, string, string, string) error
	Me(context.Context, string) (User, error)
}

package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/outbound"
)

type Client struct {
	apiKey func() string
	http   *http.Client
}

func New(apiKey func() string) *Client {
	return &Client{apiKey: apiKey, http: &http.Client{Timeout: 20 * time.Second}}
}

type findResult struct {
	MovieResults []struct {
		ID            int64   `json:"id"`
		Title         string  `json:"title"`
		OriginalTitle string  `json:"original_title"`
		Overview      string  `json:"overview"`
		PosterPath    string  `json:"poster_path"`
		BackdropPath  string  `json:"backdrop_path"`
		VoteAverage   float64 `json:"vote_average"`
		VoteCount     int     `json:"vote_count"`
	} `json:"movie_results"`
	TVResults []struct {
		ID           int64   `json:"id"`
		Name         string  `json:"name"`
		OriginalName string  `json:"original_name"`
		Overview     string  `json:"overview"`
		PosterPath   string  `json:"poster_path"`
		BackdropPath string  `json:"backdrop_path"`
		VoteAverage  float64 `json:"vote_average"`
		VoteCount    int     `json:"vote_count"`
	} `json:"tv_results"`
	TVEpisodeResults []struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		ShowID int64  `json:"show_id"`
	} `json:"tv_episode_results"`
}

func (c *Client) Lookup(ctx context.Context, imdbID string, kind domain.MediaKind, language, fallback string) (domain.CatalogMetadata, error) {
	if c.apiKey() == "" {
		return domain.CatalogMetadata{}, fmt.Errorf("TMDB API key is not configured")
	}
	if imdbID == "" {
		return domain.CatalogMetadata{}, fmt.Errorf("IMDb id is unavailable")
	}
	primary, err := c.find(ctx, imdbID, language)
	if err != nil {
		return domain.CatalogMetadata{}, err
	}
	m := selectResult(primary, kind, language)
	if (m.Title == "" || m.Overview == "") && fallback != "" && fallback != language {
		secondary, fallbackErr := c.find(ctx, imdbID, fallback)
		if fallbackErr == nil {
			other := selectResult(secondary, kind, fallback)
			if m.ProviderID == "" {
				m = other
			} else {
				if m.Title == "" {
					m.Title = other.Title
				}
				if m.OriginalTitle == "" {
					m.OriginalTitle = other.OriginalTitle
				}
				if m.Overview == "" {
					m.Overview = other.Overview
				}
				if m.PosterPath == "" {
					m.PosterPath = other.PosterPath
				}
				if m.BackdropPath == "" {
					m.BackdropPath = other.BackdropPath
				}
			}
		}
	}
	if m.ProviderID == "" {
		return domain.CatalogMetadata{}, fmt.Errorf("TMDB did not match %s (requested kind %s; movie results %d, TV results %d, episode results %d)", imdbID, kind, len(primary.MovieResults), len(primary.TVResults), len(primary.TVEpisodeResults))
	}
	now := time.Now().UTC()
	m.Provider, m.FetchedAt, m.ExpiresAt = "tmdb", now, now.Add(30*24*time.Hour)
	return m, nil
}

func (c *Client) find(ctx context.Context, imdbID, language string) (findResult, error) {
	key := c.apiKey()
	endpoint := "https://api.themoviedb.org/3/find/" + url.PathEscape(imdbID) + "?external_source=imdb_id&language=" + url.QueryEscape(language)
	if !strings.HasPrefix(key, "eyJ") {
		endpoint += "&api_key=" + url.QueryEscape(key)
	}
	resp, err := outbound.Do(ctx, c.http, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(key, "eyJ") {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("Accept", "application/json")
		return req, nil
	}, outbound.Policy{Provider: "TMDB", Attempts: 3, MaxInlineDelay: 15 * time.Second})
	if err != nil {
		return findResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return findResult{}, fmt.Errorf("TMDB returned HTTP %d", resp.StatusCode)
	}
	var result findResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil {
		return findResult{}, fmt.Errorf("decode TMDB response: %w", err)
	}
	return result, nil
}

func selectResult(result findResult, kind domain.MediaKind, language string) domain.CatalogMetadata {
	if kind == domain.MediaSeries && len(result.TVResults) > 0 {
		return tvMetadata(result, language)
	}
	if kind == domain.MediaMovie && len(result.MovieResults) > 0 {
		return movieMetadata(result, language)
	}
	// FileList names are parsed before metadata exists, so the inferred media
	// kind is a useful preference rather than an authority. TMDB's Find API can
	// validly return a TV record for a release parsed as a movie (and vice
	// versa); retaining that result is more accurate than reporting no match.
	if len(result.TVResults) > 0 {
		return tvMetadata(result, language)
	}
	if len(result.MovieResults) > 0 {
		return movieMetadata(result, language)
	}
	return domain.CatalogMetadata{}
}

func tvMetadata(result findResult, language string) domain.CatalogMetadata {
	x := result.TVResults[0]
	return domain.CatalogMetadata{ProviderID: strconv.FormatInt(x.ID, 10), Title: x.Name, OriginalTitle: x.OriginalName, Overview: x.Overview, PosterPath: x.PosterPath, BackdropPath: x.BackdropPath, Language: language, Rating: x.VoteAverage, RatingVotes: x.VoteCount, RatingProvider: "tmdb"}
}

func movieMetadata(result findResult, language string) domain.CatalogMetadata {
	x := result.MovieResults[0]
	return domain.CatalogMetadata{ProviderID: strconv.FormatInt(x.ID, 10), Title: x.Title, OriginalTitle: x.OriginalTitle, Overview: x.Overview, PosterPath: x.PosterPath, BackdropPath: x.BackdropPath, Language: language, Rating: x.VoteAverage, RatingVotes: x.VoteCount, RatingProvider: "tmdb"}
}

func (c *Client) OpenArtwork(ctx context.Context, path, kind string) (io.ReadCloser, string, error) {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return nil, "", fmt.Errorf("invalid TMDB artwork path")
	}
	size := "w500"
	if kind == "backdrop" {
		size = "w1280"
	}
	address := "https://image.tmdb.org/t/p/" + size + path
	resp, err := outbound.Do(ctx, c.http, func() (*http.Request, error) { return http.NewRequestWithContext(ctx, http.MethodGet, address, nil) }, outbound.Policy{Provider: "TMDB artwork", Attempts: 3, MaxInlineDelay: 10 * time.Second})
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, "", fmt.Errorf("TMDB image returned HTTP %d", resp.StatusCode)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

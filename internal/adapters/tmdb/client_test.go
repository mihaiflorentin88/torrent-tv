package tmdb

import (
	"encoding/json"
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

func TestSelectResultFallsBackWhenParsedKindDisagrees(t *testing.T) {
	var result findResult
	if err := json.Unmarshal([]byte(`{
		"tv_results":[{"id":123,"name":"Correct TV title","original_name":"Original","overview":"Overview","vote_average":8.2,"vote_count":42}],
		"movie_results":[{"id":456,"title":"Correct movie title","original_title":"Original movie","overview":"Movie overview","vote_average":7.1,"vote_count":12}]
	}`), &result); err != nil {
		t.Fatal(err)
	}

	if got := selectResult(findResult{TVResults: result.TVResults}, domain.MediaMovie, "en-US"); got.Title != "Correct TV title" || got.ProviderID != "123" {
		t.Fatalf("movie-classified TV result was discarded: %#v", got)
	}
	if got := selectResult(findResult{MovieResults: result.MovieResults}, domain.MediaSeries, "en-US"); got.Title != "Correct movie title" || got.ProviderID != "456" {
		t.Fatalf("series-classified movie result was discarded: %#v", got)
	}
}

func TestSelectResultStillPrefersRequestedKind(t *testing.T) {
	var result findResult
	if err := json.Unmarshal([]byte(`{"tv_results":[{"id":1,"name":"TV"}],"movie_results":[{"id":2,"title":"Movie"}]}`), &result); err != nil {
		t.Fatal(err)
	}
	if got := selectResult(result, domain.MediaSeries, "en-US"); got.Title != "TV" {
		t.Fatalf("series preference returned %#v", got)
	}
	if got := selectResult(result, domain.MediaMovie, "en-US"); got.Title != "Movie" {
		t.Fatalf("movie preference returned %#v", got)
	}
}

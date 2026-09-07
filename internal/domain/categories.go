package domain

import "strings"

type Category struct {
	ID                 int    `json:"id"`
	Name               string `json:"name"`
	Artwork            string `json:"artwork"`
	Episodic           bool   `json:"episodic"`
	DefaultBlacklisted bool   `json:"defaultBlacklisted"`
}

var Categories = []Category{{24, "Anime", "animation", true, false}, {11, "Music", "music", false, false}, {29, "Books", "learning", false, true}, {30, "Courses", "learning", false, true}, {15, "Cartoons", "animation", true, false}, {18, "Various", "other", false, true}, {16, "Docs", "learning", false, true}, {25, "Movies 3D", "movies", false, false}, {6, "Movies 4K", "movies", false, false}, {26, "Movies 4K Blu-Ray", "movies", false, false}, {20, "Movies Blu-Ray", "movies", false, false}, {2, "Movies DVD", "movies", false, false}, {3, "Movies DVD-RO", "movies", false, false}, {4, "Movies HD", "movies", false, false}, {19, "Movies HD-RO", "movies", false, false}, {1, "Movies SD", "movies", false, false}, {5, "FLAC", "music", false, false}, {10, "Games Console", "software", false, true}, {9, "Games PC", "software", false, true}, {31, "K-Drama", "television", true, false}, {17, "Linux", "software", false, true}, {22, "Mobile", "software", false, true}, {8, "Apps", "software", false, true}, {28, "RO Dubbed", "television", true, false}, {27, "TV-Series 4K", "television", true, false}, {21, "TV-Series HD", "television", true, false}, {23, "TV-Series SD", "television", true, false}, {13, "Sport", "sport", false, false}, {12, "Videoclips", "music", false, false}, {7, "XXX", "other", false, true}}

func DefaultBlacklistedCategory(name string) bool {
	for _, category := range Categories {
		if strings.EqualFold(category.Name, name) {
			return category.DefaultBlacklisted
		}
	}
	return false
}

// CategoryBrowseClass maps a FileList category to the broad browsing class
// used for discovery eligibility: video, audio, software, games, or other.
// Adapters and the repository projection must agree on this mapping so
// category descriptors and persisted rows never diverge.
func CategoryBrowseClass(c Category) string {
	if strings.HasPrefix(c.Name, "Games") {
		return "games"
	}
	if c.Name == "XXX" {
		return "adult"
	}
	switch c.Artwork {
	case "movies", "animation", "television", "sport":
		return "video"
	case "music":
		return "audio"
	case "software":
		return "software"
	default:
		return "other"
	}
}

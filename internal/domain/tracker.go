package domain

import "errors"

type TrackerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TrackerCategory struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	BrowseClass string `json:"browseClass"`
	Excluded    bool   `json:"excluded"`
}

type TorrentAcquisition struct {
	Metainfo []byte
	Magnet   string
}

func (a TorrentAcquisition) Validate() error {
	if (len(a.Metainfo) == 0) == (a.Magnet == "") {
		return errors.New("acquisition requires exactly one of metainfo or magnet")
	}
	if len(a.Metainfo) >= 16<<20 {
		return errors.New("torrent metadata is too large")
	}
	return nil
}

var (
	ErrTrackerDisabled     = errors.New("tracker is disabled")
	ErrTrackerUnconfigured = errors.New("tracker is not configured")
	ErrMagnetUnsupported   = errors.New("new magnets require qBittorrent 4.5.0 or newer, or the native engine")
)

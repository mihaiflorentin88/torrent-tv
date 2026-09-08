package nativetorrent

import (
	"time"

	"github.com/anacrolix/torrent"
)

// speedMeter keeps an exponential moving average of per-second download
// deltas for one torrent.
type speedMeter struct {
	prev int64
	ema  float64
}

func (m *speedMeter) sample(read int64) float64 {
	delta := read - m.prev
	if delta < 0 {
		delta = 0 // counters can restart when a torrent is re-verified
	}
	m.prev = read
	m.ema = 0.7*m.ema + 0.3*float64(delta)
	return m.ema
}

// speedLoop samples every torrent's useful data counter once per second.
func (c *Client) speedLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.mu.Lock()
			var torrents []*torrent.Torrent
			if c.privateClient != nil {
				torrents = append(torrents, c.privateClient.Torrents()...)
			}
			if c.publicClient != nil {
				torrents = append(torrents, c.publicClient.Torrents()...)
			}
			for _, t := range torrents {
				hash := t.InfoHash().HexString()
				m := c.speeds[hash]
				if m == nil {
					m = &speedMeter{}
					c.speeds[hash] = m
				}
				stats := t.Stats()
				m.sample(stats.BytesReadData.Int64())
				up := c.uploadSpeeds[hash]
				if up == nil {
					up = &speedMeter{}
					c.uploadSpeeds[hash] = up
				}
				up.sample(stats.BytesWrittenData.Int64())
			}
			c.mu.Unlock()
		}
	}
}

func (c *Client) stopSpeedLoop() {
	// Close is called explicitly and again via test cleanup; closing an
	// already-closed stop channel would panic.
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
}

func (c *Client) currentSpeed(hash string) int64 {
	if m := c.speeds[hash]; m != nil {
		return int64(m.ema)
	}
	return 0
}

func (c *Client) currentUploadSpeed(hash string) int64 {
	if m := c.uploadSpeeds[hash]; m != nil {
		return int64(m.ema)
	}
	return 0
}

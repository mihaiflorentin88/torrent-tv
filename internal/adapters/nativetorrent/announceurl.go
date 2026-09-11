package nativetorrent

import (
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// preferUDP4 pins a udp:// announce URL to IPv4 by rewriting its scheme to
// udp4://, the pinned library's v4-forcing scheme: the tracker announcer
// then binds an IPv4 socket and resolves A records only. The deployment
// host has no global IPv6 route, so the library's dual-stack "udp" handling
// loses every AAAA-first sendto with "network is unreachable". udp6, http,
// https, ws and already-rewritten udp4 URLs pass through unchanged.
func preferUDP4(u string) string {
	if len(u) >= len("udp://") && strings.EqualFold(u[:len("udp://")], "udp://") {
		return "udp4://" + u[len("udp://"):]
	}
	return u
}

// forceUDP4Trackers rewrites every udp:// announce URL in the metainfo to
// udp4:// in place, before the native engine turns the metainfo into a
// torrent spec. Only announce strings change: InfoBytes is carried
// verbatim, so the info hash — the swarm identity — cannot change. The
// rewrite is idempotent, so re-applying it to persisted metainfo on every
// session load never alters an already-healed entry.
func forceUDP4Trackers(mi *metainfo.MetaInfo) {
	mi.Announce = preferUDP4(mi.Announce)
	for _, tier := range mi.AnnounceList {
		for i, u := range tier {
			tier[i] = preferUDP4(u)
		}
	}
}

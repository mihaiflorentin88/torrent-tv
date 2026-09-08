package nativetorrent

// Wire-level discovery regression suite. Everything here runs on loopback:
// a controlled DHT node and raw-TCP BitTorrent peers stand in for the
// internet. No trackers, no x.pe shortcuts, no config-field assertions —
// every claim is an observation of real wire behavior or live client state.

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	dht "github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// --- controlled DHT node -------------------------------------------------

type memoryPeerStore struct {
	mu    sync.RWMutex
	peers map[metainfo.Hash][]krpc.NodeAddr
}

func newMemoryPeerStore() *memoryPeerStore {
	return &memoryPeerStore{peers: make(map[metainfo.Hash][]krpc.NodeAddr)}
}

func (s *memoryPeerStore) AddPeer(ih metainfo.Hash, na krpc.NodeAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.peers[ih] {
		if bytes.Equal(existing.IP, na.IP) && existing.Port == na.Port {
			return
		}
	}
	s.peers[ih] = append(s.peers[ih], na)
}

func (s *memoryPeerStore) GetPeers(ih metainfo.Hash) []krpc.NodeAddr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]krpc.NodeAddr(nil), s.peers[ih]...)
}

// dhtQueryRecord is one query observed at the controlled node.
type dhtQueryRecord struct {
	Method   string
	InfoHash metainfo.Hash // set for get_peers/announce_peer
	Source   net.Addr
	Port     int
	Implied  bool
}
type announceRecord struct {
	InfoHash metainfo.Hash
	IP       net.IP
	Port     int
}

// controlledNode is a single-loopback DHT server with full wire recording.
// NoSecurity is REQUIRED: DHT security extensions derive node IDs from IPs
// and reject loopback-derived IDs outright.
type controlledNode struct {
	srv *dht.Server

	mu        sync.Mutex
	queries   []dhtQueryRecord
	announces []announceRecord
}

func (n *controlledNode) recordQuery(m *krpc.Msg, source net.Addr) {
	n.mu.Lock()
	defer n.mu.Unlock()
	rec := dhtQueryRecord{Method: m.Q, Source: source}
	if m.A != nil {
		rec.InfoHash = metainfo.Hash(m.A.InfoHash)
		if m.A.Port != nil {
			rec.Port = *m.A.Port
		}
		rec.Implied = m.A.ImpliedPort
	}
	n.queries = append(n.queries, rec)
}

func (n *controlledNode) snapshot() (queries []dhtQueryRecord, announces []announceRecord) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]dhtQueryRecord(nil), n.queries...), append([]announceRecord(nil), n.announces...)
}

// queriesReferencing reports queries with the given method and infohash.
func (n *controlledNode) queriesReferencing(method string, ih metainfo.Hash) []dhtQueryRecord {
	queries, _ := n.snapshot()
	var out []dhtQueryRecord
	for _, q := range queries {
		if q.Method == method && q.InfoHash == ih {
			out = append(out, q)
		}
	}
	return out
}

// announceCount returns the number of announce_peer deliveries for ih.
func (n *controlledNode) announceCount(ih metainfo.Hash) int {
	_, announces := n.snapshot()
	count := 0
	for _, a := range announces {
		if a.InfoHash == ih {
			count++
		}
	}
	return count
}

// waitForAnnounce polls until the node has recorded an announce_peer for ih.
func (n *controlledNode) waitForAnnounce(t *testing.T, ih metainfo.Hash, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if n.announceCount(ih) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	queries, _ := n.snapshot()
	t.Fatalf("controlled node never recorded announce_peer for %s; recorded queries: %+v", ih.HexString(), queries)
}

// waitForQueryFrom polls until the node has observed a query from addr.
func (n *controlledNode) waitForQueryFrom(t *testing.T, addr net.Addr, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		queries, _ := n.snapshot()
		for _, q := range queries {
			if q.Source.String() == addr.String() {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("controlled node never observed any query from %s", addr)
}

// controlledDHTNode starts one loopback DHT server whose StartingNodes point
// back at itself: nothing outside 127.0.0.1 is ever contacted.
func controlledDHTNode(t *testing.T) *controlledNode {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	n := &controlledNode{}
	ps := newMemoryPeerStore()
	srv, err := dht.NewServer(&dht.ServerConfig{
		Conn:       conn,
		NoSecurity: true,
		PeerStore:  ps,
		StartingNodes: func() ([]dht.Addr, error) {
			return []dht.Addr{dht.NewAddr(conn.LocalAddr())}, nil
		},
		OnQuery: func(query *krpc.Msg, source net.Addr) bool {
			n.recordQuery(query, source)
			return true
		},
		OnAnnouncePeer: func(ih metainfo.Hash, ip net.IP, port int, _ bool) {
			n.mu.Lock()
			n.announces = append(n.announces, announceRecord{InfoHash: ih, IP: ip, Port: port})
			n.mu.Unlock()
			ps.AddPeer(ih, krpc.NodeAddr{IP: ip, Port: port})
		},
	})
	t.Cleanup(func() { srv.Close() })
	n.srv = srv
	return n
}

func staticStartingNodes(addrs ...net.Addr) dht.StartingNodesGetter {
	return func() ([]dht.Addr, error) {
		out := make([]dht.Addr, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, dht.NewAddr(a))
		}
		return out, nil
	}
}

// --- seeder with a real DHT server ---------------------------------------

// startDhtSeeder starts a bare library client seeding root, armed with a
// real DHT server bootstrapped from the controlled node, and announces the
// torrent into that node's peer store. The test fails if the announce never
// lands: every later claim of "discovered via DHT" depends on it.
func startDhtSeeder(t *testing.T, mi metainfo.MetaInfo, root string, node *controlledNode) {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	cfg.SetListenAddr("127.0.0.1:0")
	cfg.DisableIPv6 = true
	cfg.NoDefaultPortForwarding = true
	cfg.DataDir = filepath.Dir(root)
	cfg.Seed = true
	cfg.AlwaysWantConns = true
	cfg.MaxAllocPeerRequestDataPerConn = 1 << 20
	cfg.DhtStartingNodes = func(string) dht.StartingNodesGetter {
		return staticStartingNodes(node.srv.Addr())
	}
	cfg.ConfigureAnacrolixDhtServer = func(sc *dht.ServerConfig) {
		sc.NoSecurity = true
		sc.PeerStore = newMemoryPeerStore()
		sc.StartingNodes = staticStartingNodes(node.srv.Addr())
	}
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	spec, err := torrent.TorrentSpecFromMetaInfoErr(&mi)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := cl.AddTorrentSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-st.GotInfo():
	case <-time.After(5 * time.Second):
		t.Fatal("seeder metadata never became ready")
	}
	if err := st.VerifyData(); err != nil {
		t.Fatal(err)
	}
	var srv torrent.DhtServer
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		servers := cl.DhtServers()
		if len(servers) > 0 {
			srv = servers[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if srv == nil {
		t.Fatal("seeder client never started a DHT server")
	}
	tcpAddr := cl.ListenAddrs()[0].(*net.TCPAddr)
	ann, err := srv.Announce(mi.HashInfoBytes(), tcpAddr.Port, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ann.Close()
	go func() {
		for range ann.Peers() {
		}
	}()
	node.waitForAnnounce(t, mi.HashInfoBytes(), 15*time.Second)
}

// --- raw-TCP BitTorrent peer harness --------------------------------------

const harnessPexExtID = 11

// pexPeer is a raw-TCP BitTorrent peer speaking just enough protocol to
// exchange extended (LTEP) messages: handshake, extended handshake, ut_pex.
type pexPeer struct {
	conn net.Conn

	mu       sync.Mutex
	mdict    map[pp.ExtensionName]pp.ExtensionNumber // the client's advertised m-dict
	received map[pp.ExtensionNumber][][]byte         // ext id -> payloads read from the client
}

func handshakeReserved() [8]byte {
	var r [8]byte
	r[5] |= 0x10 // BEP 10 extension protocol
	return r
}

// dialPexPeer connects to addr, completes the BitTorrent handshake for ih
// and starts a reader goroutine that records every extended message the
// client sends, including the client's extended handshake m-dict.
func dialPexPeer(t *testing.T, addr string, ih metainfo.Hash) *pexPeer {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p := &pexPeer{conn: conn, mdict: map[pp.ExtensionName]pp.ExtensionNumber{}, received: map[pp.ExtensionNumber][][]byte{}}

	var buf bytes.Buffer
	buf.WriteByte(19)
	buf.WriteString("BitTorrent protocol")
	reserved := handshakeReserved()
	buf.Write(reserved[:])
	buf.Write(ih[:])
	buf.Write([]byte("-TT0001-pexharness0-")) // 20-byte peer id
	if _, err := conn.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}

	echo := make([]byte, 68)
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(echo[28:48], ih[:]) {
		t.Fatalf("client handshake infohash mismatch: got %x want %x", echo[28:48], ih[:])
	}

	go func() {
		for {
			var lenBuf [4]byte
			if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
				return
			}
			length := binary.BigEndian.Uint32(lenBuf[:])
			if length == 0 {
				continue // keep-alive
			}
			body := make([]byte, length)
			if _, err := io.ReadFull(conn, body); err != nil {
				return
			}
			if body[0] != 20 { // not Extended; ignore bitfield/keep-alive traffic
				continue
			}
			extID := body[1]
			payload := body[2:]
			p.mu.Lock()
			if extID == pp.HandshakeExtendedID {
				var hs pp.ExtendedHandshakeMessage
				if err := bencode.Unmarshal(payload, &hs); err == nil {
					for name, id := range hs.M {
						p.mdict[name] = id
					}
				}
			}
			p.received[pp.ExtensionNumber(extID)] = append(p.received[pp.ExtensionNumber(extID)], payload)
			p.mu.Unlock()
		}
	}()
	return p
}

// sendExtendedHandshake advertises our ut_pex support and listen port.
func (p *pexPeer) sendExtendedHandshake(t *testing.T, port int) {
	t.Helper()
	msg := struct {
		M    map[string]byte `bencode:"m"`
		P    int             `bencode:"p"`
		V    string          `bencode:"v"`
		Reqq int             `bencode:"reqq"`
	}{M: map[string]byte{"ut_pex": harnessPexExtID}, P: port, V: "pex-harness", Reqq: 250}
	p.sendExtended(t, pp.HandshakeExtendedID, bencode.MustMarshal(msg))
}

func (p *pexPeer) sendExtended(t *testing.T, extID byte, payload []byte) {
	t.Helper()
	body := append([]byte{20, extID}, payload...)
	var buf bytes.Buffer
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(body)))
	buf.Write(lenBuf[:])
	buf.Write(body)
	if _, err := p.conn.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
}

// sendUtPex sends a crafted ut_pex message adding the compact peer addr.
func (p *pexPeer) sendUtPex(t *testing.T, extID byte, added []byte) {
	t.Helper()
	msg := pp.PexMsg{Added: compactAddrs(added), AddedFlags: []pp.PexPeerFlags{0}}
	p.sendExtended(t, extID, bencode.MustMarshal(msg))
}

// clientPexID returns the ut_pex extension id the client advertised, if any.
func (p *pexPeer) clientPexID() (byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id, ok := p.mdict[pp.ExtensionNamePex]
	return byte(id), ok
}

// waitForExt polls the recorded inbound traffic until at least one message
// with the given ext id has arrived, returning the matched payloads.
func (p *pexPeer) waitForExt(t *testing.T, extID byte, timeout time.Duration) [][]byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got := append([][]byte(nil), p.received[pp.ExtensionNumber(extID)]...)
		p.mu.Unlock()
		if len(got) > 0 {
			return got
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no extended message with id %d arrived within %s", extID, timeout)
	return nil
}

// assertNoExt verifies no message with the given ext id arrives before the
// deadline elapses.
func (p *pexPeer) assertNoExt(t *testing.T, window time.Duration, extIDs ...byte) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		for _, id := range extIDs {
			if len(p.received[pp.ExtensionNumber(id)]) > 0 {
				p.mu.Unlock()
				t.Fatalf("unexpected extended message id %d arrived", id)
			}
		}
		p.mu.Unlock()
		time.Sleep(25 * time.Millisecond)
	}
}

func compactAddrs(packed []byte) (out krpc.CompactIPv4NodeAddrs) {
	if err := out.UnmarshalBinary(packed); err != nil {
		panic(err)
	}
	return out
}

func compactAddrBytes(ip net.IP, port int) []byte {
	out := make([]byte, 6)
	copy(out, ip.To4())
	binary.BigEndian.PutUint16(out[4:], uint16(port))
	return out
}

// decoyListener accepts TCP connections forever and counts them: any accept
// proves the engine dialed the decoy address learned over the wire.
type decoyListener struct {
	ln net.Listener

	mu    sync.Mutex
	conns int
}

func newDecoyListener(t *testing.T) *decoyListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	d := &decoyListener{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
			d.mu.Lock()
			d.conns++
			d.mu.Unlock()
		}
	}()
	return d
}

func (d *decoyListener) addr() net.Addr { return d.ln.Addr() }

func (d *decoyListener) acceptCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns
}

// waitForDial polls until the decoy has accepted at least one connection.
func (d *decoyListener) waitForDial(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.acceptCount() > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("engine never dialed the decoy listener at %s", d.addr())
}

// assertNoDial verifies nothing dials the decoy within the window.
func (d *decoyListener) assertNoDial(t *testing.T, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if n := d.acceptCount(); n > 0 {
			t.Fatalf("decoy listener accepted %d connections, want 0", n)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// --- client factories -----------------------------------------------------

// extRecorder records (ext id, payload) for every extension message the
// client reads off the wire, via Callbacks.PeerConnReadExtensionMessage.
type extRecorder struct {
	mu   sync.Mutex
	msgs map[pp.ExtensionNumber][][]byte
}

func newExtRecorder() *extRecorder { return &extRecorder{msgs: map[pp.ExtensionNumber][][]byte{}} }

func (r *extRecorder) handler(event torrent.PeerConnReadExtensionMessageEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs[event.ExtensionNumber] = append(r.msgs[event.ExtensionNumber], event.Payload)
}

func (r *extRecorder) payloads(id pp.ExtensionNumber) [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.msgs[id]...)
}

// loopbackFactory returns a factory that forces every underlying client onto
// loopback with trackers disabled. When dhtNodes is non-nil it additionally
// arms the DHT-enabled (public) config with the controlled node and tightens
// its DHT server for loopback. PEX stays at each config's production value.
func loopbackFactory(dhtNodes *controlledNode) func(*torrent.ClientConfig) (*torrent.Client, error) {
	return func(cfg *torrent.ClientConfig) (*torrent.Client, error) {
		cfg.SetListenAddr("127.0.0.1:0")
		cfg.DisableIPv6 = true
		cfg.DisableTrackers = true
		cfg.NoDefaultPortForwarding = true
		cfg.AlwaysWantConns = true
		if dhtNodes != nil && !cfg.NoDHT {
			cfg.DhtStartingNodes = func(string) dht.StartingNodesGetter {
				return staticStartingNodes(dhtNodes.srv.Addr())
			}
			cfg.ConfigureAnacrolixDhtServer = func(sc *dht.ServerConfig) {
				sc.NoSecurity = true
			}
		}
		return torrent.NewClient(cfg)
	}
}

// newDiscoveryClient builds a facade whose public client is DHT-armed at the
// controlled node (when node != nil) and whose extension traffic is recorded
// from the PEX-enabled client only.
func newDiscoveryClient(t *testing.T, node *controlledNode, rec *extRecorder) *Client {
	t.Helper()
	factory := loopbackFactory(node)
	if rec != nil {
		base := factory
		factory = func(cfg *torrent.ClientConfig) (*torrent.Client, error) {
			cfg.Callbacks.PeerConnReadExtensionMessage = []func(torrent.PeerConnReadExtensionMessageEvent){rec.handler}
			return base(cfg)
		}
	}
	dir := t.TempDir()
	c, err := newWithTorrentClient(Config{
		DataDir:        filepath.Join(dir, "data"),
		SessionDir:     filepath.Join(dir, "session"),
		PeerPort:       0,
		PublicPeerPort: 0,
		Readahead:      1 << 20,
		StartWindow:    1 << 20,
	}, factory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// dhtServerAddr waits for the client's first DHT server and returns its UDP
// address.
func dhtServerAddr(t *testing.T, cl *torrent.Client, timeout time.Duration) net.Addr {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if servers := cl.DhtServers(); len(servers) > 0 {
			return servers[0].Addr()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("client never started a DHT server")
	return nil
}

// listenAddr returns the client's first TCP listen address.
func listenAddr(t *testing.T, cl *torrent.Client) net.Addr {
	t.Helper()
	addrs := cl.ListenAddrs()
	if len(addrs) == 0 {
		t.Fatal("client has no listen address")
	}
	return addrs[0]
}

func magnetSourceFree(ih metainfo.Hash) string {
	return fmt.Sprintf("magnet:?xt=urn:btih:%s", ih.HexString())
}

// verifyDownloaded compares every seeded file byte-for-byte against the
// content the engine wrote under its infohash directory.
func verifyDownloaded(t *testing.T, dataDir, hash, root string) {
	t.Helper()
	downloaded := filepath.Join(dataDir, hash)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		want, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(filepath.Join(downloaded, filepath.Base(root), rel))
		if err != nil {
			return err
		}
		if !bytes.Equal(want, got) {
			return fmt.Errorf("%s: downloaded bytes differ from seed content", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// --- tests ----------------------------------------------------------------

// TestPublicClientDiscoversSeederViaDHTAndDownloads proves the full public
// path: a tracker-less, x.pe-less magnet resolves purely through the
// controlled DHT node, and the resulting payload transfers hash-verified.
func TestPublicClientDiscoversSeederViaDHTAndDownloads(t *testing.T) {
	root := seedContent(t)
	mi, _ := buildPublicTestMetainfo(t, root)
	ih := mi.HashInfoBytes()

	node := controlledDHTNode(t)
	startDhtSeeder(t, mi, root, node)

	c := newDiscoveryClient(t, node, nil)
	engineDHT := dhtServerAddr(t, c.publicClient, 10*time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	raw, err := c.ResolveMagnet(ctx, magnetSourceFree(ih), t.TempDir(), domain.MagnetDiscoveryPublic)
	if err != nil {
		queries, announces := node.snapshot()
		t.Fatalf("ResolveMagnet failed: %v; queries: %+v; announces: %+v", err, queries, announces)
	}

	// The exported metadata must be the seeder's, byte-equal at the info
	// level — proving a real ut_metadata transfer, not local synthesis.
	resolved, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.HashInfoBytes() != ih {
		t.Fatalf("resolved infohash %s, want %s", resolved.HashInfoBytes(), ih)
	}
	if !bytes.Equal(resolved.InfoBytes, mi.InfoBytes) {
		t.Fatal("resolved info differs from seeded info")
	}

	// Wire proof the metadata rode the controlled DHT: the engine's DHT
	// server must have issued get_peers for this infohash at the node.
	if node.queriesReferencing("get_peers", ih) == nil {
		t.Fatal("engine never issued get_peers for the infohash at the controlled node")
	}
	_ = engineDHT

	// Re-add the raw metainfo: public classification, then a real
	// hash-verified payload transfer over the discovered peer connection.
	hash, err := c.Add(ctx, bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	files, err := c.Files(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	var mediaIndices, subIndices []int
	for _, f := range files {
		if f.Playable {
			mediaIndices = append(mediaIndices, f.Index)
		} else {
			subIndices = append(subIndices, f.Index)
		}
	}
	if err := c.PrepareFiles(ctx, hash, mediaIndices, subIndices); err != nil {
		t.Fatal(err)
	}
	if pt, ok := c.publicClient.Torrent(ih); ok && len(c.publicClient.DhtServers()) > 0 {
		_, stop, err := pt.AnnounceToDht(c.publicClient.DhtServers()[0])
		if err == nil {
			defer stop()
		}
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		pm, err := c.Pieces(ctx, hash)
		if err != nil {
			t.Fatal(err)
		}
		allComplete := true
		for _, s := range pm.States {
			if s != 2 {
				allComplete = false
				break
			}
		}
		if allComplete {
			break
		}
		if time.Now().After(deadline.Add(-100 * time.Millisecond)) {
			pt, _ := c.publicClient.Torrent(ih)
			var stats torrent.TorrentStats
			if pt != nil {
				stats = pt.Stats()
			}
			t.Fatalf("pieces never reached state 2; states: %v; stats: %+v", pm.States[:min(20, len(pm.States))], stats)
		}
		time.Sleep(50 * time.Millisecond)
	}
	verifyDownloaded(t, c.dataDir, hash, root)
}

// TestPrivateTorrentNeverTouchesDHT proves a private torrent is wire-silent
// toward DHT: while the public client's DHT is armed and observable at the
// controlled node, the private torrent generates no get_peers, no
// announce_peer, and gains no peers. (The shared public client's bootstrap
// control traffic, present before the private torrent is added, is not the
// private torrent's — the infohash-scoped assertions below are what the
// private torrent must keep silent.)
func TestPrivateTorrentNeverTouchesDHT(t *testing.T) {
	root := seedContent(t)
	mi, raw := buildTestMetainfo(t, root)
	ih := mi.HashInfoBytes()

	node := controlledDHTNode(t)
	c := newDiscoveryClient(t, node, nil)

	// Prove the observability of the channel: the public client's DHT
	// server really reaches the controlled node.
	engineDHT := dhtServerAddr(t, c.publicClient, 10*time.Second)
	node.waitForQueryFrom(t, engineDHT, 15*time.Second)
	_ = engineDHT

	hash, err := c.Add(t.Context(), bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 40 {
		t.Fatalf("unexpected hash %q", hash)
	}

	// Bounded observation window: the private torrent must stay peerless
	// and the node must never see its infohash referenced by any query.
	window := 4 * time.Second
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		pt, ok := c.privateClient.Torrent(ih)
		if !ok || pt == nil {
			t.Fatal("private torrent not registered in the private client")
		}
		if n := pt.Stats().TotalPeers; n != 0 {
			t.Fatalf("private torrent gained %d peers", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if qs := node.queriesReferencing("get_peers", ih); len(qs) != 0 {
		t.Fatalf("controlled node saw %d get_peers for the private infohash", len(qs))
	}
	if qs := node.queriesReferencing("announce_peer", ih); len(qs) != 0 {
		t.Fatalf("controlled node saw %d announce_peer for the private infohash", len(qs))
	}
	queries, _ := node.snapshot()
	for _, q := range queries {
		if q.Method != "announce_peer" {
			continue
		}
		for _, a := range []net.Addr{engineDHT, listenAddr(t, c.privateClient), listenAddr(t, c.publicClient)} {
			if q.Source.String() == a.String() {
				t.Fatalf("engine address %s issued announce_peer at the controlled node", a)
			}
		}
	}

	// A private-only engine built through the factory seam has no DHT at
	// all: DhtServers() must be empty on both underlying clients.
	factory := loopbackFactory(nil)
	privateOnlyFactory := func(cfg *torrent.ClientConfig) (*torrent.Client, error) {
		cfg.NoDHT = true
		return factory(cfg)
	}
	dir := t.TempDir()
	privateOnly, err := newWithTorrentClient(Config{
		DataDir:        filepath.Join(dir, "data"),
		SessionDir:     filepath.Join(dir, "session"),
		PeerPort:       0,
		PublicPeerPort: 0,
		Readahead:      1 << 20,
		StartWindow:    1 << 20,
	}, privateOnlyFactory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = privateOnly.Close() }()
	if n := len(privateOnly.privateClient.DhtServers()); n != 0 {
		t.Fatalf("private client has %d DHT servers, want 0", n)
	}
	if n := len(privateOnly.publicClient.DhtServers()); n != 0 {
		t.Fatalf("public client has %d DHT servers, want 0", n)
	}
}

// TestPublicClientExchangesUtPex is the positive control for PEX: the public
// client ingests a crafted ut_pex from harness peer A (dialing the decoy),
// and relays learned addresses to harness peer B as outbound ut_pex.
func TestPublicClientExchangesUtPex(t *testing.T) {
	root := seedContent(t)
	_, raw := buildPublicTestMetainfo(t, root)

	rec := newExtRecorder()
	c := newDiscoveryClient(t, nil, rec)
	hash, err := c.Add(t.Context(), bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	var ihRaw metainfo.Hash
	if err := ihRaw.FromHexString(hash); err != nil {
		t.Fatal(err)
	}
	files, err := c.Files(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareFiles(t.Context(), hash, []int{files[0].Index}, nil); err != nil {
		t.Fatal(err)
	}

	decoy := newDecoyListener(t)
	clientAddr := listenAddr(t, c.publicClient).String()

	// Peer A: advertises ut_pex, then feeds the client a crafted PEX
	// message adding the decoy.
	peerA := dialPexPeer(t, clientAddr, ihRaw)
	peerA.sendExtendedHandshake(t, peerA.conn.LocalAddr().(*net.TCPAddr).Port)
	_ = peerA.waitForExt(t, pp.HandshakeExtendedID, 10*time.Second)
	aID, aHasPEX := peerA.clientPexID()
	if !aHasPEX {
		t.Fatal("public client extended handshake omits ut_pex")
	}
	peerA.sendUtPex(t, aID, compactAddrBytes(decoy.addr().(*net.TCPAddr).IP, decoy.addr().(*net.TCPAddr).Port))

	// Positive control: the client dials the decoy it just learned.
	decoy.waitForDial(t, 15*time.Second)

	// The recorder saw the crafted ut_pex we sent (id equals the client's
	// own local ut_pex id, since inbound dispatch is by local map).
	sawCrafted := false
	for _, payload := range rec.payloads(pp.ExtensionNumber(aID)) {
		if bytes.Contains(payload, compactAddrBytes(decoy.addr().(*net.TCPAddr).IP, decoy.addr().(*net.TCPAddr).Port)) {
			sawCrafted = true
		}
	}
	if !sawCrafted {
		t.Fatal("recorder never saw the crafted ut_pex payload reach the client")
	}

	// Peer B: connected after A was learned; the client's PEX writer must
	// relay the learned address to B as outbound ut_pex (the first flush
	// fires immediately, retries every 10s).
	peerB := dialPexPeer(t, clientAddr, ihRaw)
	peerB.sendExtendedHandshake(t, peerB.conn.LocalAddr().(*net.TCPAddr).Port)
	peerB.waitForExt(t, pp.HandshakeExtendedID, 10*time.Second)
	aCompact := compactAddrBytes(peerA.conn.LocalAddr().(*net.TCPAddr).IP, peerA.conn.LocalAddr().(*net.TCPAddr).Port)
	deadline := time.Now().Add(30 * time.Second)
	relayed := false
	for time.Now().Before(deadline) && !relayed {
		for _, payload := range peerB.waitForExt(t, harnessPexExtID, 5*time.Second) {
			if bytes.Contains(payload, aCompact) {
				relayed = true
			}
		}
	}
	if !relayed {
		t.Fatal("peer B never received the learned address via outbound ut_pex")
	}
}

// TestPrivateClientIgnoresUtPex proves PEX isolation on the wire: the
// private client's extended handshake omits ut_pex, a crafted inbound
// ut_pex is delivered to the client (recorder) yet ignored — no decoy dial,
// and nothing relayed to a second peer.
func TestPrivateClientIgnoresUtPex(t *testing.T) {
	root := seedContent(t)
	_, raw := buildTestMetainfo(t, root)

	rec := newExtRecorder()
	c := newDiscoveryClient(t, nil, rec)
	hash, err := c.Add(t.Context(), bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	var ihRaw metainfo.Hash
	if err := ihRaw.FromHexString(hash); err != nil {
		t.Fatal(err)
	}
	files, err := c.Files(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareFiles(t.Context(), hash, []int{files[0].Index}, nil); err != nil {
		t.Fatal(err)
	}

	decoy := newDecoyListener(t)
	clientAddr := listenAddr(t, c.privateClient).String()

	// Wire-observable gating: the private client's m-dict omits ut_pex.
	peerA := dialPexPeer(t, clientAddr, ihRaw)
	peerA.sendExtendedHandshake(t, peerA.conn.LocalAddr().(*net.TCPAddr).Port)
	peerA.waitForExt(t, pp.HandshakeExtendedID, 10*time.Second)
	if _, hasPEX := peerA.clientPexID(); hasPEX {
		t.Fatal("private client extended handshake advertises ut_pex")
	}

	// Crafted inbound ut_pex under a plausible id: the client reads it
	// (recorder proves delivery) and must ignore it.
	peerA.sendUtPex(t, harnessPexExtID, compactAddrBytes(decoy.addr().(*net.TCPAddr).IP, decoy.addr().(*net.TCPAddr).Port))
	delivered := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !delivered {
		for _, payload := range rec.payloads(harnessPexExtID) {
			if bytes.Contains(payload, compactAddrBytes(decoy.addr().(*net.TCPAddr).IP, decoy.addr().(*net.TCPAddr).Port)) {
				delivered = true
			}
		}
		if !delivered {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !delivered {
		t.Fatal("crafted ut_pex never reached the client; isolation claim would be vacuous")
	}

	// The learned decoy must never be dialed and never relayed.
	decoy.assertNoDial(t, 2*time.Second)
	peerB := dialPexPeer(t, clientAddr, ihRaw)
	peerB.sendExtendedHandshake(t, peerB.conn.LocalAddr().(*net.TCPAddr).Port)
	peerB.waitForExt(t, pp.HandshakeExtendedID, 10*time.Second)
	peerB.assertNoExt(t, 2*time.Second, harnessPexExtID, 1)
}

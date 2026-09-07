package application

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

type stubEngine struct {
	calls int
}

func (s *stubEngine) Test(context.Context) (string, error) { s.calls++; return "", nil }
func (s *stubEngine) Add(context.Context, io.Reader, string) (string, error) {
	s.calls++
	return "", nil
}

func (s *stubEngine) Files(context.Context, string) ([]domain.TorrentFile, error) {
	s.calls++
	return nil, nil
}

func (s *stubEngine) Status(context.Context, string) (domain.DownloadStatus, error) {
	s.calls++
	return domain.DownloadStatus{}, nil
}

func (s *stubEngine) Pieces(context.Context, string) (domain.PieceMap, error) {
	s.calls++
	return domain.PieceMap{}, nil
}

func (s *stubEngine) PrepareFile(context.Context, string, int, []int) error { s.calls++; return nil }

func (s *stubEngine) PrepareFiles(context.Context, string, []int, []int) error { s.calls++; return nil }

func (s *stubEngine) PrepareRange(context.Context, string, int, int64, int64) error {
	s.calls++
	return nil
}
func (s *stubEngine) Pause(context.Context, string) error        { s.calls++; return nil }
func (s *stubEngine) Resume(context.Context, string) error       { s.calls++; return nil }
func (s *stubEngine) Remove(context.Context, string, bool) error { s.calls++; return nil }
func (s *stubEngine) ResolveMagnet(context.Context, string, string) ([]byte, error) {
	s.calls++
	return nil, nil
}

func newTestEngineSet() (*EngineSet, *stubEngine, *stubEngine) {
	native := &stubEngine{}
	qb := &stubEngine{}
	es, err := NewEngineSet("native:", map[string]TorrentEngine{"native:": native, "qb:": qb})
	if err != nil {
		panic(err)
	}
	return es, native, qb
}

func TestEngineSetResolve(t *testing.T) {
	es, native, qb := newTestEngineSet()

	eng, hash, ok := es.Resolve("native:h1")
	if !ok || hash != "h1" || eng != TorrentEngine(native) {
		t.Fatalf("Resolve(native:h1) = %v, %q, %v; want native engine, h1, true", eng, hash, ok)
	}

	eng, hash, ok = es.Resolve("qb:h2")
	if !ok || hash != "h2" || eng != TorrentEngine(qb) {
		t.Fatalf("Resolve(qb:h2) = %v, %q, %v; want qb engine, h2, true", eng, hash, ok)
	}
}

func TestEngineSetResolveUnknownPrefix(t *testing.T) {
	es, native, qb := newTestEngineSet()

	eng, hash, ok := es.Resolve("foo:h")
	if ok || hash != "" || eng != nil {
		t.Fatalf("Resolve(foo:h) = %v, %q, %v; want nil, \"\", false", eng, hash, ok)
	}
	if native.calls != 0 || qb.calls != 0 {
		t.Fatalf("default engine received %d calls on unknown prefix; want 0", native.calls)
	}
}

func TestEngineSetDefault(t *testing.T) {
	es, native, _ := newTestEngineSet()
	if es.Default() != TorrentEngine(native) {
		t.Fatal("Default() did not return the engine registered under defaultPrefix")
	}
	if es.DefaultPrefix() != "native:" {
		t.Fatalf("DefaultPrefix() = %q; want native:", es.DefaultPrefix())
	}
}

func TestEngineSetMarkUnavailableRoundTrip(t *testing.T) {
	es, _, _ := newTestEngineSet()

	boom := errors.New("boom")
	if _, ok := es.InitError("qb:"); ok {
		t.Fatal("InitError reported an error before MarkUnavailable")
	}

	// Marking unavailable removes the engine from routing.
	if _, _, ok := es.Resolve("qb:h"); !ok {
		t.Fatal("Resolve(qb:h) expected ok before MarkUnavailable")
	}
	es.MarkUnavailable("qb:", boom)
	if _, _, ok := es.Resolve("qb:h"); ok {
		t.Fatal("Resolve(qb:h) succeeded after MarkUnavailable; want false")
	}

	got, ok := es.InitError("qb:")
	if !ok || !errors.Is(got, boom) {
		t.Fatalf("InitError(qb:) = %v, %v; want boom, true", got, ok)
	}
}

func TestEngineSetEachAndPrefixes(t *testing.T) {
	native := &stubEngine{}
	qb := &stubEngine{}
	es, err := NewEngineSet("qb:", map[string]TorrentEngine{"native:": native, "qb:": qb})
	if err != nil {
		t.Fatalf("NewEngineSet: %v", err)
	}

	var order []string
	es.Each(func(prefix string, engine TorrentEngine) {
		order = append(order, prefix)
	})
	if len(order) != 2 || order[0] != "native:" || order[1] != "qb:" {
		t.Fatalf("Each order = %v; want [native: qb:]", order)
	}

	prefixes := es.Prefixes()
	if len(prefixes) != 2 || prefixes[0] != "native:" || prefixes[1] != "qb:" {
		t.Fatalf("Prefixes() = %v; want [native: qb:]", prefixes)
	}
}

func TestEngineSetConstructorValidation(t *testing.T) {
	native := &stubEngine{}
	qb := &stubEngine{}

	if _, err := NewEngineSet("native:", nil); err == nil {
		t.Fatal("NewEngineSet accepted an empty engines map")
	}
	if _, err := NewEngineSet("qb:", map[string]TorrentEngine{"native:": native}); err == nil {
		t.Fatal("NewEngineSet accepted a default prefix missing from the map")
	}
	if _, err := NewEngineSet("native", map[string]TorrentEngine{"native:": native, "qb:": qb}); err == nil {
		t.Fatal("NewEngineSet accepted a default prefix without trailing ':'")
	}
}

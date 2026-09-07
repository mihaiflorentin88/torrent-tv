package application

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// EngineSet owns the torrent engines registered under owner prefixes and
// routes "prefix:hash" routes to the engine that owns the prefix. Engines
// that failed construction are absent from the routing map; their original
// construction error is kept for callers to report.
type EngineSet struct {
	defaultPrefix string
	engines       map[string]TorrentEngine
	initErrs      map[string]error
}

func NewEngineSet(defaultPrefix string, engines map[string]TorrentEngine) (*EngineSet, error) {
	if len(engines) == 0 {
		return nil, errors.New("engines map cannot be empty")
	}
	if !strings.HasSuffix(defaultPrefix, ":") {
		return nil, fmt.Errorf("default prefix %q must end with ':'", defaultPrefix)
	}
	if _, ok := engines[defaultPrefix]; !ok {
		return nil, fmt.Errorf("default engine %q not registered in engines map", defaultPrefix)
	}
	return &EngineSet{defaultPrefix: defaultPrefix, engines: engines, initErrs: map[string]error{}}, nil
}

// Resolve splits a "prefix:hash" route and returns the engine registered
// under the prefix. ok=false means the prefix is unknown or the engine is
// not registered (unconstructed or marked unavailable); callers report the
// engine as unavailable — there is no fallback to the default.
func (es *EngineSet) Resolve(route string) (TorrentEngine, string, bool) {
	prefix, hash, ok := strings.Cut(route, ":")
	if !ok {
		return nil, "", false
	}
	engine, ok := es.engines[prefix+":"]
	if !ok {
		return nil, "", false
	}
	return engine, hash, true
}

func (es *EngineSet) Default() TorrentEngine {
	return es.engines[es.defaultPrefix]
}

func (es *EngineSet) DefaultPrefix() string {
	return es.defaultPrefix
}

// MarkUnavailable records the ORIGINAL construction error for a required
// engine that failed to construct. It does not insert a placeholder engine:
// the prefix simply stops resolving.
func (es *EngineSet) MarkUnavailable(prefix string, initErr error) {
	es.initErrs[prefix] = initErr
	delete(es.engines, prefix)
}

func (es *EngineSet) InitError(prefix string) (error, bool) {
	err, ok := es.initErrs[prefix]
	return err, ok
}

// Each calls fn for every registered engine in fixed order: "native:" first,
// then "qb:", then any remaining prefixes sorted.
func (es *EngineSet) Each(fn func(prefix string, engine TorrentEngine)) {
	for _, prefix := range es.order() {
		fn(prefix, es.engines[prefix])
	}
}

// Prefixes returns the registered prefixes in the same fixed order as Each.
func (es *EngineSet) Prefixes() []string {
	return es.order()
}

func (es *EngineSet) order() []string {
	prefixes := make([]string, 0, len(es.engines))
	for prefix := range es.engines {
		prefixes = append(prefixes, prefix)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		ri, rj := routeRank(prefixes[i]), routeRank(prefixes[j])
		if ri != rj {
			return ri < rj
		}
		return prefixes[i] < prefixes[j]
	})
	return prefixes
}

func routeRank(prefix string) int {
	switch prefix {
	case "native:":
		return 0
	case "qb:":
		return 1
	default:
		return 2
	}
}

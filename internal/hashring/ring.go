package hashring

import (
	"hash/fnv"
	"sort"
	"strconv"
	"sync"
)

// Ring is a consistent-hash ring with virtual nodes.
// Get(key) returns the same node for the same key as long as the
// healthy node set is unchanged; when a node leaves, keys remap
// only onto remaining nodes.
type Ring struct {
	mu     sync.RWMutex
	vnodes int
	points []vnode
	nodes  map[string]struct{}
}

type vnode struct {
	hash uint32
	node string
}

func New(vnodes int) *Ring {
	if vnodes <= 0 {
		vnodes = 100
	}
	return &Ring{
		vnodes: vnodes,
		nodes:  make(map[string]struct{}),
	}
}

// SetNodes rebuilds the ring from the given node names (order does not matter).
func (r *Ring) SetNodes(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nodes = make(map[string]struct{}, len(names))
	r.points = r.points[:0]
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := r.nodes[name]; ok {
			continue
		}
		r.nodes[name] = struct{}{}
		for i := 0; i < r.vnodes; i++ {
			r.points = append(r.points, vnode{
				hash: hashKey(name + "#" + strconv.Itoa(i)),
				node: name,
			})
		}
	}
	sort.Slice(r.points, func(i, j int) bool {
		if r.points[i].hash == r.points[j].hash {
			return r.points[i].node < r.points[j].node
		}
		return r.points[i].hash < r.points[j].hash
	})
}

// Get returns the node responsible for key, or "" if the ring is empty.
func (r *Ring) Get(key string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.points) == 0 {
		return ""
	}
	h := hashKey(key)
	idx := sort.Search(len(r.points), func(i int) bool {
		return r.points[i].hash >= h
	})
	if idx == len(r.points) {
		idx = 0
	}
	return r.points[idx].node
}

// Members returns a sorted copy of current node names.
func (r *Ring) Members() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.nodes))
	for name := range r.nodes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Len returns the number of physical nodes on the ring.
func (r *Ring) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}

func hashKey(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

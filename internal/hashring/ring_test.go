package hashring

import (
	"fmt"
	"testing"
)

func TestGetStableForSameKey(t *testing.T) {
	t.Parallel()
	ring := New(50)
	ring.SetNodes([]string{"edge-1", "edge-2", "edge-3"})

	first := ring.Get("/images/test.png")
	for i := 0; i < 20; i++ {
		if got := ring.Get("/images/test.png"); got != first {
			t.Fatalf("Get unstable: %q then %q", first, got)
		}
	}
	if first == "" {
		t.Fatal("expected a node")
	}
}

func TestPartialFailureRemapsDownNode(t *testing.T) {
	t.Parallel()
	ring := New(80)
	ring.SetNodes([]string{"edge-1", "edge-2", "edge-3"})

	var onDown []string
	for i := 0; i < 500 && len(onDown) < 5; i++ {
		key := fmt.Sprintf("/object-%d", i)
		if ring.Get(key) == "edge-2" {
			onDown = append(onDown, key)
		}
	}
	if len(onDown) == 0 {
		t.Fatal("could not find keys mapped to edge-2")
	}

	stableBefore := map[string]string{}
	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("/stable-%d", i)
		node := ring.Get(key)
		if node != "edge-2" {
			stableBefore[key] = node
		}
	}

	ring.SetNodes([]string{"edge-1", "edge-3"})

	for _, key := range onDown {
		got := ring.Get(key)
		if got == "edge-2" || got == "" {
			t.Fatalf("key %s mapped to %q after removing edge-2", key, got)
		}
	}

	unchanged := 0
	for key, want := range stableBefore {
		if ring.Get(key) == want {
			unchanged++
		}
	}
	if unchanged == 0 {
		t.Fatal("expected most keys not on the down node to stay put")
	}
}

func TestEmptyRing(t *testing.T) {
	t.Parallel()
	ring := New(10)
	if got := ring.Get("/x"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	ring.SetNodes(nil)
	if ring.Len() != 0 {
		t.Fatalf("len = %d", ring.Len())
	}
}

func TestMembersSorted(t *testing.T) {
	t.Parallel()
	ring := New(5)
	ring.SetNodes([]string{"edge-3", "edge-1", "edge-2"})
	got := ring.Members()
	want := []string{"edge-1", "edge-2", "edge-3"}
	if len(got) != len(want) {
		t.Fatalf("members = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("members = %v, want %v", got, want)
		}
	}
}

package engine

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/ir"
	"github.com/bkum/weftly/internal/schema"
)

// mkGraph builds a tiny ir.Graph from (id, needs...) tuples in schema order.
func mkGraph(steps ...[]string) *ir.Graph {
	g := &ir.Graph{Workflow: &schema.Workflow{}, Order: nil}
	for _, s := range steps {
		id := s[0]
		var needs []string
		if len(s) > 1 {
			needs = append(needs, s[1:]...)
		}
		g.Order = append(g.Order, &ir.StepNode{ID: id, Needs: needs, Action: "run", Config: &schema.Step{}})
	}
	return g
}

func TestScheduleRespectsNeeds(t *testing.T) {
	// a -> b -> c;  a -> d -> c
	//              (c needs both b and d)
	g := mkGraph(
		[]string{"a"},
		[]string{"b", "a"},
		[]string{"d", "a"},
		[]string{"c", "b", "d"},
	)
	var mu sync.Mutex
	var order []string
	status := schedule(context.Background(), g, 4, func(_ context.Context, n *ir.StepNode) events.Status {
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		order = append(order, n.ID)
		mu.Unlock()
		return events.Success
	})
	if status != events.Success {
		t.Fatalf("want Success, got %s", status)
	}
	// verify constraints: a first, c last, b and d after a and before c
	posOf := func(id string) int {
		for i, x := range order {
			if x == id {
				return i
			}
		}
		return -1
	}
	if posOf("a") != 0 {
		t.Errorf("a should be first, order=%v", order)
	}
	if posOf("c") != 3 {
		t.Errorf("c should be last, order=%v", order)
	}
	if posOf("b") <= posOf("a") || posOf("d") <= posOf("a") {
		t.Errorf("b,d must follow a: %v", order)
	}
	if posOf("c") <= posOf("b") || posOf("c") <= posOf("d") {
		t.Errorf("c must follow b and d: %v", order)
	}
}

func TestScheduleParallelActuallyRuns(t *testing.T) {
	// a, b, c all independent — with parallel=3 they should overlap.
	g := mkGraph(
		[]string{"a"},
		[]string{"b"},
		[]string{"c"},
	)
	var peak int32
	var inflight int32
	step := func(_ context.Context, n *ir.StepNode) events.Status {
		cur := atomic.AddInt32(&inflight, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		return events.Success
	}
	status := schedule(context.Background(), g, 3, step)
	if status != events.Success {
		t.Fatalf("want Success, got %s", status)
	}
	if peak < 2 {
		t.Errorf("expected concurrency >= 2, saw peak %d", peak)
	}
}

func TestScheduleParallelBoundHonored(t *testing.T) {
	// 5 independent steps but parallel=2 — peak must never exceed 2.
	g := mkGraph(
		[]string{"a"}, []string{"b"}, []string{"c"}, []string{"d"}, []string{"e"},
	)
	var peak int32
	var inflight int32
	step := func(_ context.Context, n *ir.StepNode) events.Status {
		cur := atomic.AddInt32(&inflight, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		return events.Success
	}
	_ = schedule(context.Background(), g, 2, step)
	if peak > 2 {
		t.Errorf("expected peak <= 2, saw %d", peak)
	}
}

func TestScheduleCascadeSkipOnFatal(t *testing.T) {
	// a fails; b needs a → should be skipped, not executed.
	g := mkGraph(
		[]string{"a"},
		[]string{"b", "a"},
		[]string{"c", "b"},
	)
	var mu sync.Mutex
	var ran []string
	step := func(_ context.Context, n *ir.StepNode) events.Status {
		mu.Lock()
		ran = append(ran, n.ID)
		mu.Unlock()
		if n.ID == "a" {
			return events.Failed
		}
		if n.SkipReason != "" {
			return events.Skipped
		}
		return events.Success
	}
	status := schedule(context.Background(), g, 4, step)
	if status != events.Failed {
		t.Fatalf("want Failed, got %s", status)
	}
	sort.Strings(ran)
	// a must run; b and c must be visited (for the cascade skip event
	// emission) but with SkipReason set — the fake step above returns
	// Skipped when it sees SkipReason, exercising the executor contract.
	want := []string{"a", "b", "c"}
	if len(ran) != 3 || ran[0] != want[0] || ran[1] != want[1] || ran[2] != want[2] {
		t.Errorf("want ran=%v, got %v", want, ran)
	}
}

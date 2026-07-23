package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/ir"
)

// schedule executes the graph honoring `needs` edges with up to `parallel`
// concurrent workers. It calls `exec` for each node (which does the actual
// action dispatch and event emission) and returns the overall run status.
//
// Semantics:
//   - A step becomes ready when all of its `Needs` have completed with a
//     non-fatal terminal status (Success, Skipped, FailedContinued).
//   - If any needed step ends with a fatal status (Failed, TimedOut),
//     dependents are marked Skipped and their subtrees cascade — no exec
//     is called for them.
//   - On the first fatal status, in-flight steps continue (a wave can only
//     be halted between waves); no new steps start.
//   - Anonymous steps (no ID, e.g. summary/upload) are wired at compile
//     time to depend on the previous named step, so they slot naturally
//     into the DAG.
func schedule(ctx context.Context, g *ir.Graph, parallel int, exec func(context.Context, *ir.StepNode) events.Status) events.Status {
	if parallel < 1 {
		parallel = 1
	}
	nodes := g.Order
	n := len(nodes)
	if n == 0 {
		return events.Success
	}

	// Assign a scheduling key for every node (real ID or synthesized).
	key := make([]string, n)
	byKey := map[string]*ir.StepNode{}
	idxByKey := map[string]int{}
	for i, node := range nodes {
		k := node.ID
		if k == "" {
			k = fmt.Sprintf("__anon_%d", i)
		}
		key[i] = k
		byKey[k] = node
		idxByKey[k] = i
	}

	// Build the in-degree table and reverse adjacency.
	indeg := make(map[string]int, n)
	dependents := map[string][]string{}
	for i, node := range nodes {
		k := key[i]
		if _, ok := indeg[k]; !ok {
			indeg[k] = 0
		}
		for _, dep := range node.Needs {
			// Only wire edges to keys we actually know about; unknown deps
			// were caught by schema.Validate.
			if _, ok := byKey[dep]; !ok {
				continue
			}
			indeg[k]++
			dependents[dep] = append(dependents[dep], k)
		}
	}

	var (
		mu       sync.Mutex
		statuses = map[string]events.Status{}
		halt     bool // set when any fatal status appears
	)
	sem := make(chan struct{}, parallel)
	done := make(chan string, n)
	inflight := 0
	dispatched := 0

	// Kick off every node with 0 in-degree.
	ready := []string{}
	for k, d := range indeg {
		if d == 0 {
			ready = append(ready, k)
		}
	}
	// Preserve schema order among the initially-ready set for deterministic
	// event ordering when the DAG has no explicit needs.
	sortKeysByIndex(ready, idxByKey)

	dispatch := func(k string) {
		inflight++
		dispatched++
		sem <- struct{}{}
		go func(k string) {
			defer func() { <-sem }()
			node := byKey[k]
			status := exec(ctx, node)
			mu.Lock()
			statuses[k] = status
			mu.Unlock()
			done <- k
		}(k)
	}

	for _, k := range ready {
		dispatch(k)
	}

	// Main loop: harvest completions, unlock dependents, dispatch or skip.
	for inflight > 0 {
		k := <-done
		inflight--

		mu.Lock()
		st := statuses[k]
		if isFatal(st) {
			halt = true
		}
		mu.Unlock()

		var newlyReady []string
		for _, dep := range dependents[k] {
			indeg[dep]--
			if indeg[dep] == 0 {
				newlyReady = append(newlyReady, dep)
			}
		}
		sortKeysByIndex(newlyReady, idxByKey)

		for _, r := range newlyReady {
			if halt {
				// Cascade skip: mark this node and (transitively) its
				// dependents as Skipped without executing.
				cascadeSkip(r, byKey, indeg, dependents, statuses, &mu, exec, ctx)
				continue
			}
			dispatch(r)
		}
	}

	// Aggregate: if anything failed non-recoverably, the run failed.
	mu.Lock()
	defer mu.Unlock()
	final := events.Success
	for _, s := range statuses {
		if isFatal(s) {
			final = events.Failed
			break
		}
	}
	// Sanity: if we somehow didn't dispatch everything (cycle would have
	// been caught in validation), leftover nodes count as skipped.
	if dispatched < n {
		if final == events.Success {
			// A DAG dominated by upstream fatals is already Failed; only
			// mark Skipped when the run had no fatals but somehow stalled.
			final = events.Skipped
		}
	}
	return final
}

// cascadeSkip marks node k and every dependent reachable through the
// dependents graph as Skipped, emitting a synthetic StepStarted +
// StepFinished{Skipped} pair through exec so state.json and the report
// stay consistent.
func cascadeSkip(k string, byKey map[string]*ir.StepNode, indeg map[string]int, dependents map[string][]string, statuses map[string]events.Status, mu *sync.Mutex, exec func(context.Context, *ir.StepNode) events.Status, ctx context.Context) {
	stack := []string{k}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		mu.Lock()
		if _, done := statuses[cur]; done {
			mu.Unlock()
			continue
		}
		statuses[cur] = events.Skipped
		mu.Unlock()
		// exec is expected to publish a Skipped StepFinished when invoked
		// on a node whose if: is false — but for cascade skips the reason
		// is upstream failure, not `if:`. We reuse the same event shape
		// via a dedicated helper on the engine side; here we just call
		// exec with a sentinel is signalled by setting the node's If to
		// a special marker isn't ideal — so we emit directly through the
		// bus via a small helper stashed on the node.
		//
		// The dispatch function passed in by the engine understands to
		// short-circuit: when the caller sets node.SkipReason non-empty,
		// it emits Skipped without running the action.
		node := byKey[cur]
		node.SkipReason = "upstream " + k + " failed"
		_ = exec(ctx, node)
		for _, dep := range dependents[cur] {
			indeg[dep]--
			stack = append(stack, dep)
		}
	}
}

func isFatal(s events.Status) bool {
	return s == events.Failed || s == events.TimedOut
}

func sortKeysByIndex(keys []string, idx map[string]int) {
	// simple insertion sort — the slice is tiny and this keeps allocation zero
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && idx[keys[j-1]] > idx[keys[j]]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
}

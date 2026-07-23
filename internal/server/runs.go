package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	_ "github.com/bkum/weftly/internal/actions" // register built-ins

	"github.com/bkum/weftly/internal/engine"
	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/schema"
)

// runRecord bundles per-run state the server tracks in memory. Every
// server-side subscriber (SSE listener, state watcher) reads through this
// record; the on-disk state.json + report.html are still written by the
// engine's normal subscribers.
type runRecord struct {
	ID       string
	Workflow string
	Inputs   map[string]any

	mu       sync.RWMutex
	events   []events.Event // append-only replay log
	closed   bool           // true once RunFinished has been observed
	waiters  []chan events.Event
	finished chan struct{}
}

func newRunRecord(id, workflow string, inputs map[string]any) *runRecord {
	return &runRecord{
		ID:       id,
		Workflow: workflow,
		Inputs:   inputs,
		finished: make(chan struct{}),
	}
}

// handle is the run's bus subscriber. It appends to the replay log and
// fans out to any live SSE waiters, closing them once RunFinished arrives.
func (r *runRecord) handle(e events.Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	waiters := append([]chan events.Event(nil), r.waiters...)
	if _, ok := e.(events.RunFinished); ok {
		r.closed = true
	}
	r.mu.Unlock()

	for _, w := range waiters {
		// non-blocking send; a slow reader drops events, they'll refresh
		// from state.json on reconnect.
		select {
		case w <- e:
		default:
		}
	}
	if r.closed {
		close(r.finished)
		r.mu.Lock()
		for _, w := range r.waiters {
			close(w)
		}
		r.waiters = nil
		r.mu.Unlock()
	}
}

// subscribe returns a snapshot of the replay log plus a channel that
// receives subsequent live events. If the run is already closed, the
// channel is returned closed (and the snapshot contains everything).
func (r *runRecord) subscribe() ([]events.Event, <-chan events.Event) {
	r.mu.Lock()
	snap := append([]events.Event(nil), r.events...)
	if r.closed {
		ch := make(chan events.Event)
		close(ch)
		r.mu.Unlock()
		return snap, ch
	}
	ch := make(chan events.Event, 128)
	r.waiters = append(r.waiters, ch)
	r.mu.Unlock()
	return snap, ch
}

type runManager struct {
	baseDir string
	log     *slog.Logger

	mu   sync.RWMutex
	runs map[string]*runRecord
}

func newRunManager(baseDir string, log *slog.Logger) *runManager {
	return &runManager{baseDir: baseDir, log: log, runs: map[string]*runRecord{}}
}

// start dispatches a workflow into a fresh run and returns its record.
// The engine runs in a goroutine; the caller can immediately open an SSE
// stream that will replay any events already emitted plus receive live ones.
//
// Note: the run intentionally uses a detached context (context.Background)
// so that returning from the POST /runs handler doesn't cancel the run.
// The run's lifetime is bound to the process, not the initiating request.
func (m *runManager) start(_ context.Context, wfID string, wf *schema.Workflow, inputs map[string]any) (*runRecord, error) {
	rec := newRunRecord("", wfID, inputs)
	bus := events.NewBus()
	bus.Subscribe(rec.handle)

	// Peek the run-id from RunStarted by inserting a one-shot subscriber.
	idCh := make(chan string, 1)
	var once sync.Once
	bus.Subscribe(func(e events.Event) {
		if s, ok := e.(events.RunStarted); ok {
			once.Do(func() { idCh <- s.RunID })
		}
	})

	errCh := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), wf, engine.Options{
			BaseDir: m.baseDir,
			Inputs:  inputs,
			Bus:     bus,
		})
		errCh <- err
	}()

	select {
	case rec.ID = <-idCh:
	case err := <-errCh:
		if err != nil {
			return nil, fmt.Errorf("run failed to start: %w", err)
		}
		return nil, errors.New("run finished before emitting RunStarted")
	case <-time.After(5 * time.Second):
		return nil, errors.New("timed out waiting for RunStarted")
	}
	m.mu.Lock()
	m.runs[rec.ID] = rec
	m.mu.Unlock()
	return rec, nil
}

func (m *runManager) get(id string) *runRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.runs[id]
}

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bkum/weftly/internal/actions"
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
	closed   bool           // set once engine.Run has fully returned
	waiters  []chan events.Event
	finished chan struct{}
	// cancel is the CancelFunc for the engine's run context. Invoking
	// it kills the in-flight run — the step's cmd.Cancel wired via
	// exec.CommandContext propagates SIGKILL to the process group.
	// Set once by runManager.start and never nil for an active run.
	cancel context.CancelFunc
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
// fans out to any live SSE waiters. It does NOT close the waiter channels
// on RunFinished — that job belongs to markClosed, which the runManager
// calls only after engine.Run has fully returned. Reason: other bus
// subscribers (state.Writer, report) may still be flushing state.json /
// report.html when RunFinished lands on this subscriber; closing the SSE
// stream before those writes finish lets a client race the disk and
// observe a "running" status in a GET /runs/:id that follows immediately.
func (r *runRecord) handle(e events.Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	waiters := append([]chan events.Event(nil), r.waiters...)
	r.mu.Unlock()

	for _, w := range waiters {
		// Non-blocking send; a slow reader drops events, they'll refresh
		// from state.json on reconnect.
		select {
		case w <- e:
		default:
		}
	}
}

// markClosed flips the record to "no more events, close all SSE waiters".
// Called by runManager once engine.Run has fully returned — guarantees
// state.Writer and report.Write have completed before any SSE reader can
// observe end-of-stream and pivot to the state endpoint.
func (r *runRecord) markClosed() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	waiters := r.waiters
	r.waiters = nil
	r.mu.Unlock()
	close(r.finished)
	for _, w := range waiters {
		close(w)
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
	store   actions.RemoteArtifactStore // may be nil

	mu   sync.RWMutex
	runs map[string]*runRecord
}

func newRunManager(baseDir string, log *slog.Logger, store actions.RemoteArtifactStore) *runManager {
	return &runManager{baseDir: baseDir, log: log, store: store, runs: map[string]*runRecord{}}
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

	// Peek the run-id from RunStarted by inserting a one-shot subscriber
	// AT THE FRONT of the fan-out. It doesn't observe payload state, so
	// ordering vs the other subscribers is irrelevant.
	idCh := make(chan string, 1)
	var once sync.Once
	bus.Subscribe(func(e events.Event) {
		if s, ok := e.(events.RunStarted); ok {
			once.Do(func() { idCh <- s.RunID })
		}
	})

	// Detached context — the HTTP request may finish before the run
	// does. Cancelling `runCtx` from handleCancelRun is how the DELETE
	// endpoint terminates a live run.
	runCtx, cancel := context.WithCancel(context.Background())
	rec.cancel = cancel

	errCh := make(chan error, 1)
	go func() {
		// rec.handle is passed as PostSubscribers so it fires AFTER
		// engine's own state.Writer + report subscribers. That
		// guarantees a client receiving RunFinished from the SSE
		// stream sees state.json/report.html already flushed to disk
		// when it pivots to GET /runs/:id.
		_, err := engine.Run(runCtx, wf, engine.Options{
			BaseDir:         m.baseDir,
			Inputs:          inputs,
			Bus:             bus,
			ArtifactStore:   m.store,
			PostSubscribers: []func(events.Event){rec.handle},
			Logger:          m.log,
		})
		errCh <- err
		// Belt and braces: even with post-subscriber ordering, close the
		// waiter channels once engine.Run returns so blocked SSE readers
		// unblock even if the last event was somehow lost.
		rec.markClosed()
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

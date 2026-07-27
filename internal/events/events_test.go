package events

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBusPublishFansOutToAllSubscribers(t *testing.T) {
	b := NewBus()
	var a, c int32
	b.Subscribe(func(e Event) { atomic.AddInt32(&a, 1) })
	b.Subscribe(func(e Event) { atomic.AddInt32(&c, 1) })
	b.Publish(RunStarted{RunID: "r1"})
	b.Publish(RunFinished{Status: Success})
	if a != 2 || c != 2 {
		t.Fatalf("want both subs to see 2 events, got a=%d c=%d", a, c)
	}
}

func TestBusUnsubscribeStopsDelivery(t *testing.T) {
	b := NewBus()
	var got int32
	unsub := b.Subscribe(func(e Event) { atomic.AddInt32(&got, 1) })
	b.Publish(RunStarted{})
	unsub()
	b.Publish(RunStarted{})
	if got != 1 {
		t.Fatalf("want 1 event after unsubscribe, got %d", got)
	}
}

func TestBusPublishIsConcurrencySafe(t *testing.T) {
	b := NewBus()
	var got int64
	b.Subscribe(func(e Event) { atomic.AddInt64(&got, 1) })
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.Publish(RunStarted{})
			}
		}()
	}
	wg.Wait()
	if got != 3200 {
		t.Fatalf("want 3200 events, got %d", got)
	}
}

func TestStepFinishedMarshalsErrAsString(t *testing.T) {
	ev := StepFinished{StepID: "a", Status: Failed, Err: errors.New("boom")}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out["Err"] != "boom" {
		t.Fatalf("want Err=boom, got %v (raw=%s)", out["Err"], string(b))
	}
}

func TestStepFinishedOmitsEmptyErr(t *testing.T) {
	ev := StepFinished{StepID: "a", Status: Success}
	b, _ := json.Marshal(ev)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if _, ok := out["Err"]; ok {
		t.Fatalf("want Err omitted when nil, got %s", string(b))
	}
}

func TestStepRetryMarshalsErrAsString(t *testing.T) {
	ev := StepRetry{StepID: "a", Attempt: 1, Of: 3, Err: errors.New("504")}
	b, _ := json.Marshal(ev)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out["Err"] != "504" {
		t.Fatalf("want Err=504, got %v", out["Err"])
	}
}

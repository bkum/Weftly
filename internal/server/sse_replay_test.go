package server_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bkum/weftly/internal/server"
)

// TestSSEHonoursLastEventID replays the same run twice — the second
// stream sends Last-Event-ID matching the last id from the first. The
// second stream must NOT re-emit the events the client already saw
// (which was the SPA "infinite retries" bug — a reconnected stream
// re-appended every StepLog line, so the UI showed each attempt N
// times and looked like an internal loop).
func TestSSEHonoursLastEventID(t *testing.T) {
	dir, wfID := writeCatalogue(t)
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})

	// Kick a run and let it finish so both stream reads get the full
	// event log (no live events after subscribe).
	body, _ := json.Marshal(map[string]any{"workflow": wfID})
	req, _ := http.NewRequest("POST", ts.URL+"/runs", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer tk")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.RunID == "" {
		t.Fatal("empty run_id")
	}

	// Wait for RunFinished so the record is fully populated.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rreq, _ := http.NewRequest("GET", ts.URL+"/runs/"+created.RunID, nil)
		rreq.Header.Set("Authorization", "Bearer tk")
		rresp, err := http.DefaultClient.Do(rreq)
		if err != nil {
			t.Fatal(err)
		}
		var st struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(rresp.Body).Decode(&st)
		rresp.Body.Close()
		if st.Status != "" && st.Status != "running" && st.Status != "pending" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	firstIDs, firstStreams := drainSSE(t, ts.URL, "tk", created.RunID, "")
	if len(firstIDs) == 0 {
		t.Fatal("first stream returned no events")
	}
	lastID := firstIDs[len(firstIDs)-1]

	secondIDs, secondStreams := drainSSE(t, ts.URL, "tk", created.RunID, strconv.Itoa(lastID))
	if len(secondIDs) != 0 {
		t.Errorf("reconnect with Last-Event-ID=%d should have zero replay events, got %d\nsecond stream:\n%s", lastID, len(secondIDs), secondStreams)
	}
	_ = firstStreams // kept for debug output on failure
}

// drainSSE opens the SSE stream, reads until the connection closes
// naturally (after RunFinished the server closes the live channel and
// the writer goroutine returns), and returns the sequence ids in the
// order received plus the raw stream body for debug.
func drainSSE(t *testing.T, base, tok, id, lastEventID string) ([]int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/runs/"+id+"/events?token="+tok, nil)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("SSE GET: %d", resp.StatusCode)
	}
	// Read a bounded slice — the run is already finished so the server
	// will send everything in one flush burst.
	deadline := time.Now().Add(2 * time.Second)
	var (
		ids  []int
		body strings.Builder
	)
	scan := bufio.NewScanner(resp.Body)
	scan.Buffer(make([]byte, 64*1024), 1024*1024)
	sawFinished := false
	for scan.Scan() && time.Now().Before(deadline) {
		line := scan.Text()
		body.WriteString(line + "\n")
		if strings.HasPrefix(line, "id: ") {
			if n, err := strconv.Atoi(strings.TrimPrefix(line, "id: ")); err == nil {
				ids = append(ids, n)
			}
		}
		if strings.Contains(line, `"RunFinished"`) {
			sawFinished = true
		}
		// After RunFinished + one blank line the record is closed and
		// the server drops the live channel; bail so the test doesn't
		// hang on the keepalive tick.
		if sawFinished && line == "" {
			// Read a couple more lines in case the connection close is
			// slightly deferred.
			break
		}
	}
	return ids, body.String()
}

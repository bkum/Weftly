package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/expr"
	"github.com/bkum/weftly/internal/secrets"
)

func TestNotifyPostsSlackShapedPayload(t *testing.T) {
	var gotBody map[string]any
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cfg := mustParseYAML(t, `
url: `+srv.URL+`
message: hello world
`)
	sc := &StepContext{
		Config:  cfg,
		Expr:    expr.New(),
		ExprEnv: expr.Env{},
		Secrets: secrets.NewRegistry(),
		Emit:    func(events.Event) {},
	}
	outs, err := notifyAction{}.Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if outs["status"] != 200 {
		t.Errorf("status: got %v", outs["status"])
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type: got %q", gotContentType)
	}
	if gotBody["text"] != "hello world" {
		t.Errorf("body text: got %v", gotBody["text"])
	}
}

func TestNotifyRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", 503)
	}))
	defer srv.Close()

	cfg := mustParseYAML(t, `
url: `+srv.URL+`
message: hi
`)
	sc := &StepContext{
		Config:  cfg,
		Expr:    expr.New(),
		ExprEnv: expr.Env{},
		Secrets: secrets.NewRegistry(),
		Emit:    func(events.Event) {},
	}
	_, err := notifyAction{}.Run(context.Background(), sc)
	if err == nil || err.Error() == "" {
		t.Fatalf("expected non-2xx error, got %v", err)
	}
}

func TestNotifyCustomPayload(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
	}))
	defer srv.Close()

	cfg := mustParseYAML(t, `
url: `+srv.URL+`
payload:
  channel: "#ops"
  text: "custom"
`)
	sc := &StepContext{
		Config:  cfg,
		Expr:    expr.New(),
		ExprEnv: expr.Env{},
		Secrets: secrets.NewRegistry(),
		Emit:    func(events.Event) {},
	}
	if _, err := (notifyAction{}).Run(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	if gotBody["channel"] != "#ops" || gotBody["text"] != "custom" {
		t.Errorf("custom payload mismatch: %+v", gotBody)
	}
}

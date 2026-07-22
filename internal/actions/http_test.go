package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/expr"
	"gopkg.in/yaml.v3"
)

func mustParseYAML(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(src), &n); err != nil {
		t.Fatalf("yaml parse: %v", err)
	}
	if n.Kind != yaml.DocumentNode || len(n.Content) == 0 {
		t.Fatalf("expected doc node")
	}
	return n.Content[0]
}

func TestHTTPActionGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "UP", "checks": []any{"db", "queue"}})
	}))
	defer srv.Close()

	cfg := mustParseYAML(t, `
GET: `+srv.URL+`/rest/health
`)
	sc := &StepContext{
		StepID:  "h",
		Config:  cfg,
		Expr:    expr.New(),
		ExprEnv: expr.Env{},
	}
	outs, err := httpAction{}.Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if outs == nil {
		t.Fatal("nil outputs")
	}
	if sc.Response == nil {
		t.Fatal("Response not set")
	}
	body := sc.Response.(map[string]any)["body"].(map[string]any)
	if body["status"] != "UP" {
		t.Errorf("got body %v", body)
	}
	if sc.Response.(map[string]any)["status"] != 200 {
		t.Errorf("got status %v", sc.Response)
	}
}

func TestHTTPActionPOSTJSONBody(t *testing.T) {
	var got struct {
		Name string `json:"name"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"partnerId": "p-42"})
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cfg := mustParseYAML(t, `
POST: `+srv.URL+`
body:
  name: "${{ inputs.partner }}"
`)
	sc := &StepContext{
		StepID: "h",
		Config: cfg,
		Expr:   expr.New(),
		ExprEnv: expr.Env{
			Inputs: map[string]any{"partner": "Acme"},
		},
	}
	if _, err := (httpAction{}).Run(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Acme" {
		t.Fatalf("body not passed with interpolation: %q", got.Name)
	}
	body := sc.Response.(map[string]any)["body"].(map[string]any)
	if body["partnerId"] != "p-42" {
		t.Fatalf("unexpected response body: %v", body)
	}
}

func TestHTTPActionAssertFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	cfg := mustParseYAML(t, `
GET: `+srv.URL+`
assert: response.status == 200
`)
	sc := &StepContext{Config: cfg, Expr: expr.New(), ExprEnv: expr.Env{}}
	_, err := (httpAction{}).Run(context.Background(), sc)
	if err == nil || !strings.Contains(err.Error(), "assert failed") {
		t.Fatalf("expected assert failure, got %v", err)
	}
}

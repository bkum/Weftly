package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/server"
)

// writeTwoWorkflows drops two workflows into a fresh catalogue dir. This
// gives the RBAC tests something to allow-list.
func writeTwoWorkflows(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		body := `
name: ` + name + `
steps:
  - id: hello
    run: echo hi
`
		if err := os.WriteFile(filepath.Join(dir, name+".yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// writeAuthFile drops a weftly.yaml with three tokens covering the RBAC
// matrix used across the tests: admin gets everything, ops gets the "*"
// allowlist, dev is restricted to workflow "alpha".
func writeAuthFile(t *testing.T) string {
	t.Helper()
	body := `
tokens:
  "admin-token-abcdefghijkl":
    name: admin
    roles: [admin]
  "ops-token-abcdefghijkl":
    name: ops
    roles: [ops]
  "dev-token-abcdefghijkl":
    name: dev
    roles: [dev]
roles:
  admin:
    admin: true
    workflows: "*"
  ops:
    workflows: "*"
  dev:
    workflows: [alpha]
`
	f := filepath.Join(t.TempDir(), "weftly.yaml")
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func doGet(t *testing.T, url, token string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return r, b
}

func TestRBACListWorkflowsFilteredByRole(t *testing.T) {
	catDir := writeTwoWorkflows(t)
	authFile := writeAuthFile(t)
	ts := startServer(t, server.Config{
		CatalogueDir: catDir,
		RunsDir:      t.TempDir(),
		AuthFile:     authFile,
	})

	// admin: sees both
	resp, body := doGet(t, ts.URL+"/workflows", "admin-token-abcdefghijkl")
	if resp.StatusCode != 200 {
		t.Fatalf("admin /workflows: %d %s", resp.StatusCode, string(body))
	}
	var list struct {
		Workflows []struct{ ID string }
	}
	_ = json.Unmarshal(body, &list)
	if len(list.Workflows) != 2 {
		t.Errorf("admin should see 2 workflows, got %d", len(list.Workflows))
	}

	// ops: also sees both (workflows "*")
	resp, body = doGet(t, ts.URL+"/workflows", "ops-token-abcdefghijkl")
	_ = json.Unmarshal(body, &list)
	if len(list.Workflows) != 2 {
		t.Errorf("ops should see 2 workflows, got %d", len(list.Workflows))
	}

	// dev: sees only "alpha"
	resp, body = doGet(t, ts.URL+"/workflows", "dev-token-abcdefghijkl")
	_ = json.Unmarshal(body, &list)
	if len(list.Workflows) != 1 || list.Workflows[0].ID != "alpha" {
		t.Errorf("dev should see just alpha, got %+v", list.Workflows)
	}
}

func TestRBACGetWorkflowForbiddenForDev(t *testing.T) {
	ts := startServer(t, server.Config{
		CatalogueDir: writeTwoWorkflows(t),
		RunsDir:      t.TempDir(),
		AuthFile:     writeAuthFile(t),
	})
	// dev may not read the metadata of "beta"
	resp, _ := doGet(t, ts.URL+"/workflows/beta", "dev-token-abcdefghijkl")
	if resp.StatusCode != 403 {
		t.Errorf("dev /workflows/beta: want 403, got %d", resp.StatusCode)
	}
	// dev may read "alpha"
	resp, _ = doGet(t, ts.URL+"/workflows/alpha", "dev-token-abcdefghijkl")
	if resp.StatusCode != 200 {
		t.Errorf("dev /workflows/alpha: want 200, got %d", resp.StatusCode)
	}
}

func TestRBACCreateRunForbiddenForDev(t *testing.T) {
	ts := startServer(t, server.Config{
		CatalogueDir: writeTwoWorkflows(t),
		RunsDir:      t.TempDir(),
		AuthFile:     writeAuthFile(t),
	})
	body, _ := json.Marshal(map[string]any{"workflow": "beta"})
	req, _ := http.NewRequest("POST", ts.URL+"/runs", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer dev-token-abcdefghijkl")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("dev POST /runs beta: want 403, got %d", resp.StatusCode)
	}
}

func TestRBACReloadRequiresAdmin(t *testing.T) {
	ts := startServer(t, server.Config{
		CatalogueDir: writeTwoWorkflows(t),
		RunsDir:      t.TempDir(),
		AuthFile:     writeAuthFile(t),
	})
	// ops is not admin
	req, _ := http.NewRequest("POST", ts.URL+"/reload", nil)
	req.Header.Set("Authorization", "Bearer ops-token-abcdefghijkl")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("ops /reload: want 403, got %d", resp.StatusCode)
	}
	// admin is
	req, _ = http.NewRequest("POST", ts.URL+"/reload", nil)
	req.Header.Set("Authorization", "Bearer admin-token-abcdefghijkl")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("admin /reload: want 200, got %d", resp.StatusCode)
	}
}

func TestRBACAuthFileValidation(t *testing.T) {
	tmp := t.TempDir()
	badToken := `
tokens:
  "x":
    name: t
    roles: [r]
roles:
  r:
    workflows: "*"
`
	badRoleRef := `
tokens:
  "sufficiently-long-token":
    name: t
    roles: [missing]
roles:
  r:
    workflows: "*"
`
	for name, body := range map[string]string{
		"short-token":  badToken,
		"unknown-role": badRoleRef,
	} {
		t.Run(name, func(t *testing.T) {
			f := filepath.Join(tmp, name+".yaml")
			if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := server.LoadAuthFile(f)
			if err == nil || !(strings.Contains(err.Error(), "too short") || strings.Contains(err.Error(), "unknown role")) {
				t.Errorf("expected validation error, got %v", err)
			}
		})
	}
}

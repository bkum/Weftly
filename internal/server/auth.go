package server

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Principal is what a successful Authenticator hands back. It carries
// enough identity to log the caller and enough policy metadata to enforce
// per-workflow authorization without another lookup.
type Principal struct {
	Name          string
	Roles         []string
	Admin         bool     // /reload and other admin verbs
	AllWorkflows  bool     // "*" allowlist
	WorkflowAllow []string // explicit workflow-id allowlist (used only if !AllWorkflows)
}

// Anonymous returns true for a principal with no meaningful identity —
// what BearerToken("") returns when auth is disabled.
func (p Principal) Anonymous() bool { return p.Name == "anon" || p.Name == "" }

// CanRunWorkflow reports whether the principal may start / read the given
// workflow id. Admins can always run everything; a "*" allowlist matches
// all; otherwise the id must be in WorkflowAllow.
func (p Principal) CanRunWorkflow(id string) bool {
	if p.Admin || p.AllWorkflows {
		return true
	}
	for _, w := range p.WorkflowAllow {
		if w == id {
			return true
		}
	}
	return false
}

// Authenticator decides whether a request is allowed and returns the
// resolved Principal on success.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, bool)
}

// -----------------------------------------------------------------------
// bearerToken — single-token backend, used when Config.Token is set and
// no auth file is configured. Grants full admin.
// -----------------------------------------------------------------------

type bearerAuth struct {
	token string
}

// BearerToken keeps the old constructor name so existing tests still
// compile. Empty string disables auth (principal Anonymous, all access).
func BearerToken(token string) Authenticator { return &bearerAuth{token: token} }

func (b *bearerAuth) Authenticate(r *http.Request) (Principal, bool) {
	if b.token == "" {
		return Principal{Name: "anon", Admin: true, AllWorkflows: true}, true
	}
	got := extractToken(r)
	if got == "" {
		return Principal{}, false
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(b.token)) != 1 {
		return Principal{}, false
	}
	return Principal{Name: "bearer", Admin: true, AllWorkflows: true}, true
}

// -----------------------------------------------------------------------
// rbacAuth — multi-token backend driven by weftly.yaml. Each token maps
// to a principal (name + roles); each role has an allowlist plus optional
// admin flag. Roles are OR-ed when a principal has multiple.
// -----------------------------------------------------------------------

// AuthFile is the persisted shape of the RBAC config.
//
//	tokens:
//	  "opaque-token-alice":
//	    name: alice
//	    roles: [ops, admin]
//	  "opaque-token-bob":
//	    name: bob
//	    roles: [dev]
//	roles:
//	  admin:
//	    admin: true
//	    workflows: "*"
//	  ops:
//	    workflows: "*"
//	  dev:
//	    workflows: [petclinic-onboarding, dev-smoke]
type AuthFile struct {
	Tokens map[string]TokenEntry `yaml:"tokens"`
	Roles  map[string]RoleEntry  `yaml:"roles"`
}

type TokenEntry struct {
	Name  string   `yaml:"name"`
	Roles []string `yaml:"roles"`
}

type RoleEntry struct {
	Admin     bool `yaml:"admin"`
	Workflows any  `yaml:"workflows"` // "*" or []string
	extracted RoleExtracted
}

type RoleExtracted struct {
	AllWorkflows bool
	Workflows    []string
}

// LoadAuthFile parses weftly.yaml. A missing file returns (nil, nil) so
// callers can distinguish "no RBAC configured" from a broken file.
func LoadAuthFile(path string) (*AuthFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var af AuthFile
	if err := yaml.Unmarshal(data, &af); err != nil {
		return nil, fmt.Errorf("auth file: %w", err)
	}
	if err := af.Validate(); err != nil {
		return nil, err
	}
	return &af, nil
}

// Validate checks the file for common misconfiguration; on success it
// also pre-computes each role's RoleExtracted so lookups are cheap.
func (af *AuthFile) Validate() error {
	if af == nil {
		return nil
	}
	for name, tok := range af.Tokens {
		if strings.TrimSpace(name) == "" {
			return errors.New("auth: token key must not be empty")
		}
		if len(name) < 12 {
			return fmt.Errorf("auth: token %q is too short (min 12 chars) — use a random opaque value", name)
		}
		if tok.Name == "" {
			return fmt.Errorf("auth: token entry missing name field")
		}
		for _, role := range tok.Roles {
			if _, ok := af.Roles[role]; !ok {
				return fmt.Errorf("auth: token %q references unknown role %q", tok.Name, role)
			}
		}
	}
	for name, role := range af.Roles {
		ex, err := role.extract()
		if err != nil {
			return fmt.Errorf("auth: role %q: %w", name, err)
		}
		role.extracted = ex
		af.Roles[name] = role
	}
	return nil
}

func (r RoleEntry) extract() (RoleExtracted, error) {
	switch w := r.Workflows.(type) {
	case nil:
		return RoleExtracted{}, nil
	case string:
		if w == "*" {
			return RoleExtracted{AllWorkflows: true}, nil
		}
		return RoleExtracted{}, fmt.Errorf("workflows: unexpected string %q (want \"*\" or a list)", w)
	case []any:
		out := make([]string, 0, len(w))
		for _, e := range w {
			s, ok := e.(string)
			if !ok {
				return RoleExtracted{}, fmt.Errorf("workflows: list entries must be strings")
			}
			out = append(out, s)
		}
		return RoleExtracted{Workflows: out}, nil
	}
	return RoleExtracted{}, fmt.Errorf("workflows: unexpected type %T", r.Workflows)
}

type rbacAuth struct {
	file *AuthFile
}

// RBACFromFile constructs an Authenticator backed by weftly.yaml.
func RBACFromFile(af *AuthFile) Authenticator { return &rbacAuth{file: af} }

func (a *rbacAuth) Authenticate(r *http.Request) (Principal, bool) {
	got := extractToken(r)
	if got == "" {
		return Principal{}, false
	}
	// Constant-time compare against every configured token — never leak
	// how close a bogus token came to matching.
	var match TokenEntry
	found := false
	for tok, entry := range a.file.Tokens {
		if subtle.ConstantTimeCompare([]byte(got), []byte(tok)) == 1 {
			match = entry
			found = true
		}
	}
	if !found {
		return Principal{}, false
	}
	p := Principal{Name: match.Name, Roles: match.Roles}
	for _, roleName := range match.Roles {
		role := a.file.Roles[roleName]
		if role.Admin {
			p.Admin = true
		}
		if role.extracted.AllWorkflows {
			p.AllWorkflows = true
		}
		p.WorkflowAllow = append(p.WorkflowAllow, role.extracted.Workflows...)
	}
	return p, true
}

// extractToken pulls a bearer token from either the Authorization header
// or the `?token=` query param (EventSource needs the latter).
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if q := r.URL.Query().Get("token"); q != "" {
		return q
	}
	return ""
}

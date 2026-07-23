package schema

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantErr string // substring match; "" = expect valid
	}{
		{
			name: "minimal ok",
			src: `
name: t
steps:
  - id: hi
    run: echo hi
`,
		},
		{
			name: "missing name",
			src: `
steps:
  - id: hi
    run: echo hi
`,
			wantErr: "name: required",
		},
		{
			name: "zero action keys",
			src: `
name: t
steps:
  - id: hi
    env: { X: y }
`,
			wantErr: "must declare exactly one action key",
		},
		{
			name: "multiple action keys",
			src: `
name: t
steps:
  - id: hi
    run: echo
    http:
      GET: http://example
`,
			wantErr: "exactly one action key, found",
		},
		{
			name: "assert with http is inline (allowed)",
			src: `
name: t
steps:
  - id: hi
    http:
      GET: http://example
    assert: response.status == 200
`,
		},
		{
			name: "duplicate id",
			src: `
name: t
steps:
  - id: a
    run: echo 1
  - id: a
    run: echo 2
`,
			wantErr: "duplicate id",
		},
		{
			name: "bad id chars",
			src: `
name: t
steps:
  - id: BadId
    run: echo
`,
			wantErr: "must match",
		},
		{
			name: "unknown needs",
			src: `
name: t
steps:
  - id: a
    needs: [ghost]
    run: echo
`,
			wantErr: `unknown step "ghost"`,
		},
		{
			name: "needs cycle",
			src: `
name: t
steps:
  - id: a
    needs: [b]
    run: echo
  - id: b
    needs: [a]
    run: echo
`,
			wantErr: "cycle in `needs`",
		},
		{
			name: "summary needs no id",
			src: `
name: t
steps:
  - summary: hello
`,
		},
		{
			name: "container on run is valid",
			src: `
name: t
steps:
  - id: c
    container: alpine:3.19
    run: echo hi
`,
		},
		{
			name: "retry with attempts:1 is rejected",
			src: `
name: t
steps:
  - id: x
    retry: { attempts: 1, delay: 1s }
    run: echo hi
`,
			wantErr: "attempts must be >= 2",
		},
		{
			name: "retry on: bogus is rejected",
			src: `
name: t
steps:
  - id: x
    retry: { attempts: 3, on: [banana] }
    run: echo hi
`,
			wantErr: `unknown status "banana"`,
		},
		{
			name: "retry backoff must be linear/exponential",
			src: `
name: t
steps:
  - id: x
    retry: { attempts: 3, backoff: cubic }
    run: echo hi
`,
			wantErr: "backoff must be one of",
		},
		{
			name: "retry valid",
			src: `
name: t
steps:
  - id: x
    retry: { attempts: 3, delay: 500ms, backoff: exponential, on: [failed, timed-out] }
    run: echo hi
`,
		},
		{
			name: "container on http is rejected",
			src: `
name: t
steps:
  - id: c
    container: alpine:3.19
    http:
      GET: http://example.com
`,
			wantErr: "container: is only valid on a run step",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, err := Parse(strings.NewReader(tc.src))
			if err != nil {
				if tc.wantErr == "" {
					t.Fatalf("unexpected parse error: %v", err)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parse error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			verr := Validate(wf)
			if tc.wantErr == "" {
				if verr != nil {
					t.Fatalf("expected valid, got: %v", verr)
				}
				return
			}
			if verr == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(verr.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", verr.Error(), tc.wantErr)
			}
		})
	}
}

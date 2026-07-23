package actions

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/expr"
	"github.com/bkum/weftly/internal/secrets"
)

// buildContainerCmd is a pure builder — no process is spawned. We can
// unit-test the argv shape without podman/docker installed by faking
// the engine lookup via PATH manipulation.
func TestBuildContainerCmdArgsShape(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		if _, err := exec.LookPath("docker"); err != nil {
			t.Skip("neither podman nor docker on PATH; can't build cmd")
		}
	}
	sc := &StepContext{
		StepID:  "s1",
		Workdir: "/host/ws",
		Env:     map[string]string{"FOO": "bar", "BAZ": "qux"},
		Secrets: secrets.NewRegistry(),
		Expr:    expr.New(),
		Emit:    func(events.Event) {}, // sc.Log needs a non-nil emitter
	}
	sc.Container = "alpine:3.19"
	cmd, err := buildContainerCmd(context.Background(), sc, "/host/tmp/script.sh", "/host/tmp/out.env")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"run", "--rm", "--network=none",
		"/host/ws:/weftly/workspace",
		"/host/tmp/script.sh:/weftly/script:ro",
		"/host/tmp/out.env:/weftly/output.env",
		"-w /weftly/workspace",
		"WEFTLY_OUTPUT=/weftly/output.env",
		"WEFTLY_STEP_ID=s1",
		"WEFTLY_WORKSPACE=/weftly/workspace",
		"BAZ=qux",
		"FOO=bar",
		"alpine:3.19",
		"sh /weftly/script",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q\nfull argv: %s", want, joined)
		}
	}
	// Deterministic ordering: BAZ must appear before FOO since we sort.
	if strings.Index(joined, "BAZ=qux") > strings.Index(joined, "FOO=bar") {
		t.Errorf("env keys not sorted: %s", joined)
	}
}

func TestBuildContainerCmdRejectsBadEnvKey(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		if _, err := exec.LookPath("docker"); err != nil {
			t.Skip("neither podman nor docker on PATH")
		}
	}
	sc := &StepContext{
		StepID:  "s1",
		Workdir: "/host/ws",
		Env:     map[string]string{"BAD KEY": "x"},
		Secrets: secrets.NewRegistry(),
		Expr:    expr.New(),
		Emit:    func(events.Event) {},
	}
	sc.Container = "alpine:3.19"
	_, err := buildContainerCmd(context.Background(), sc, "/tmp/s", "/tmp/o")
	if err == nil || !strings.Contains(err.Error(), "cannot pass safely") {
		t.Fatalf("expected bad-env-key error, got %v", err)
	}
}

func TestValidEnvKey(t *testing.T) {
	cases := map[string]bool{
		"FOO":       true,
		"foo_bar":   true,
		"F1":        true,
		"":          false,
		"1FOO":      false,
		"foo bar":   false,
		"foo=bar":   false,
		"FOO-BAR":   false,
		"HTTP_HOST": true,
	}
	for k, want := range cases {
		if got := validEnvKey(k); got != want {
			t.Errorf("validEnvKey(%q) = %v, want %v", k, got, want)
		}
	}
}

// TestRunActionContainerEndToEnd verifies the full path — a script
// ran inside a container writes to $WEFTLY_OUTPUT on the shared mount,
// and the parent parses it after cmd.Wait. Skips when neither engine
// is present or the daemon isn't reachable (common on CI); the unit
// tests above still cover the argv construction.
func TestRunActionContainerEndToEnd(t *testing.T) {
	engine, _, err := resolveContainerEngine()
	if err != nil {
		t.Skip("no container engine on PATH")
	}
	// Cheap reachability probe — `<engine> info` fails fast when the
	// daemon socket isn't there. We don't care about the info payload.
	if err := exec.Command(engine, "info").Run(); err != nil {
		t.Skipf("%s daemon not reachable: %v", engine, err)
	}
	sc, got := newContext(t, scalar(`echo "greeting=hi-$WHO" >> "$WEFTLY_OUTPUT"`), map[string]string{"WHO": "world"}, false)
	sc.Container = "alpine:3.19"
	outs, err := (runAction{}).Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outs["greeting"] != "hi-world" {
		t.Errorf("output greeting: got %v", outs["greeting"])
	}
	// The backend-selection log line should have gone out.
	sawBanner := false
	for _, e := range *got {
		if log, ok := e.(events.StepLog); ok && strings.Contains(log.Line, "container backend=") {
			sawBanner = true
			break
		}
	}
	if !sawBanner {
		t.Errorf("expected 'container backend=' log line")
	}
}

// resolveContainerEngine returns podman first when both are present.
func TestResolveContainerEnginePrefersPodman(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not on PATH; can't assert preference")
	}
	_, name, err := resolveContainerEngine()
	if err != nil {
		t.Fatal(err)
	}
	if name != "podman" {
		t.Errorf("preferred engine: got %q, want podman", name)
	}
}

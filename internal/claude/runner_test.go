package claude

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunnerArgsIncludeHookEvents(t *testing.T) {
	// The runner's argv must include --include-hook-events so the
	// CLI emits task_started / task_updated / hook_started /
	// hook_response system events alongside normal stream-json.
	got := buildPromptArgs(PromptOptions{})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--include-hook-events") {
		t.Fatalf("expected --include-hook-events in args, got %q", joined)
	}
}

// TestRunner_Prompt_HappyPath spawns a fake `claude` shell script
// that prints our fixture to stdout, exits 0. The Runner should
// stream every event then close the channel.
func TestRunner_Prompt_HappyPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based fake claude binary; skip on Windows")
	}
	bin := writeFakeClaude(t, fakeClaudeScript(`cat $FIXTURE`))
	fixture := absFixture(t, "simple_text_response.jsonl")

	r := &Runner{claudeBin: bin}
	t.Setenv("FIXTURE", fixture)

	pr, err := r.Prompt(context.Background(), PromptOptions{
		SessionUUID: "fake-uuid",
		CWD:         t.TempDir(),
		Prompt:      "anything",
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	defer pr.Stop()

	events := drain(t, pr.Events, 3*time.Second)
	if len(events) < 5 {
		t.Fatalf("got %d events, want at least 5; events=%v", len(events), kinds(events))
	}
	// First and last meaningful events:
	if events[0].Kind != KindSystem {
		t.Errorf("first kind = %q, want system", events[0].Kind)
	}
	var sawResult bool
	for _, ev := range events {
		if ev.Kind == KindResult {
			sawResult = true
		}
	}
	if !sawResult {
		t.Error("never saw a result event")
	}
	if err := pr.Wait(); err != nil {
		t.Errorf("Wait: %v", err)
	}
}

// TestRunner_Prompt_StopMidStream sends a fake claude script that
// emits one line, sleeps for a long time, then would emit more.
// Runner.Stop() should interrupt the script, the Events channel
// should close, and Wait() should return a non-nil error (because
// the script was killed by SIGINT).
func TestRunner_Prompt_StopMidStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based fake claude binary; skip on Windows")
	}
	bin := writeFakeClaude(t, fakeClaudeScript(`
echo '{"type":"system","subtype":"init"}'
# Block; tests will SIGINT us.
sleep 30
echo '{"type":"system","subtype":"too_late"}'
`))

	r := &Runner{claudeBin: bin}
	pr, err := r.Prompt(context.Background(), PromptOptions{
		CWD:    t.TempDir(),
		Prompt: "anything",
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// Read the first event, then Stop.
	first, ok := <-pr.Events
	if !ok {
		t.Fatal("channel closed before first event")
	}
	if first.Kind != KindSystem {
		t.Errorf("first kind = %q, want system", first.Kind)
	}

	// Stop should cause the fake script to terminate quickly.
	pr.Stop()

	// Channel should close within a second or two.
	rest := drain(t, pr.Events, 5*time.Second)
	for _, ev := range rest {
		if ev.System != nil && ev.System.Subtype == "too_late" {
			t.Error("event after Stop — SIGINT didn't reach the child")
		}
	}

	// Wait should report the non-zero exit (signal-killed processes
	// return a non-nil error in exec.Wait).
	if err := pr.Wait(); err == nil {
		t.Error("Wait returned nil, expected non-nil (process was signaled)")
	}
}

// TestRunner_Prompt_RequiresCWD verifies the bare-minimum input
// validation.
func TestRunner_Prompt_RequiresCWD(t *testing.T) {
	r := &Runner{claudeBin: "/bin/true"}
	_, err := r.Prompt(context.Background(), PromptOptions{Prompt: "x"})
	if err == nil {
		t.Fatal("Prompt without CWD: want error, got nil")
	}
}

func TestRunner_Prompt_RequiresPrompt(t *testing.T) {
	r := &Runner{claudeBin: "/bin/true"}
	_, err := r.Prompt(context.Background(), PromptOptions{CWD: t.TempDir()})
	if err == nil {
		t.Fatal("Prompt without Prompt text: want error, got nil")
	}
}

// --- helpers ---

func drain(t *testing.T, ch <-chan Event, timeout time.Duration) []Event {
	t.Helper()
	out := []Event{}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline.C:
			return out
		}
	}
}

func kinds(evs []Event) []EventKind {
	out := make([]EventKind, len(evs))
	for i, ev := range evs {
		out[i] = ev.Kind
	}
	return out
}

// writeFakeClaude writes a #!/bin/sh script to a temp dir and
// returns its absolute path. The script body is appended verbatim
// after the shebang.
func writeFakeClaude(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(body), 0700); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	return bin
}

// fakeClaudeScript wraps a body with the standard shebang.
func fakeClaudeScript(body string) string {
	return "#!/bin/sh\n" + body + "\n"
}

func absFixture(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "testdata", name)
}

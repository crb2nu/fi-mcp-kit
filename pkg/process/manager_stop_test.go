package process

import (
	"os/exec"
	"testing"
	"time"
)

// TestStopProcess_PostKillBoundUnsticksHangingWait simulates the bug that
// motivated bounding the post-SIGKILL wait: a backgrounded grandchild
// inherits the parent's stdout pipe and keeps it open long after the parent
// is killed, so cmd.Wait() blocks until the grandchild releases the pipe.
//
// Before the bound, Stop() would block for the grandchild's lifetime (here
// 30s). With the bound, Stop returns within ~postKillWait once SIGKILL has
// fired on the direct child.
func TestStopProcess_PostKillBoundUnsticksHangingWait(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-management test in short mode")
	}

	// `sh -c "sleep 30 &"` exits the shell immediately (sleep is backgrounded)
	// but the sleep grandchild inherits the parent's stdout/stderr pipes
	// and keeps them open. cmd.Wait() then blocks reading those pipes
	// until the grandchild dies — i.e. until the 30s sleep finishes.
	//
	// Critically, we attach StdoutPipe/StdinPipe like the real Manager does
	// (manager.go uses cmd.StdinPipe / cmd.StdoutPipe). Without explicit
	// pipes, Go inherits the parent's stdout fd and Wait would NOT block
	// the way it does in production.
	cmd := exec.Command("sh", "-c", "sleep 30 &")
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	if _, err := cmd.StdoutPipe(); err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	postKillWait := 500 * time.Millisecond
	start := time.Now()
	stopProcess(cmd, 50*time.Millisecond, 50*time.Millisecond, postKillWait)
	elapsed := time.Since(start)

	// Budget: 50ms (sigterm) + 50ms (grace) + postKillWait + small slop.
	budget := 50*time.Millisecond + 50*time.Millisecond + postKillWait + 500*time.Millisecond
	if elapsed > budget {
		t.Fatalf("stopProcess took %v, exceeds bound of %v — the post-SIGKILL wait is not respecting the timeout", elapsed, budget)
	}

	// Reap the grandchild to keep the test process tidy. We don't care if
	// kill fails; the test has already proven the timeout.
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func TestStopProcess_ReturnsImmediatelyOnCleanExit(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	stopProcess(cmd, 2*time.Second, 1*time.Second, 2*time.Second)
	elapsed := time.Since(start)

	// `true` exits in milliseconds; Wait should return well before the first
	// SIGTERM timeout fires.
	if elapsed > 500*time.Millisecond {
		t.Errorf("stopProcess on a fast-exit child took %v, expected < 500ms", elapsed)
	}
}

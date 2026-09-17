// Package cadtest is a fake CAD kernel process, for tests that need a kernel to be
// slow, or to die, on cue.
//
// # Why a fake process and not a real slow build
//
// The defects it exists for are about TIME and PROCESS DEATH: a build that runs
// past its limit, a process that dies mid-build. A real build123d build that is
// reliably slower than a limit is either a limit so short a busy machine trips it
// on a bracket, or a design that takes half a minute to build, twice. Either way
// the fence is load-sensitive and slow. This process answers the same protocol as
// sidecar.py in microseconds, and is slow or dies exactly when a solid asks it to.
//
// # How it runs
//
// The kernel starts `<python> <dir>/sidecar.py`. A test points "python" at its own
// test binary (FakeKernel does), and the package's TestMain calls RunIfAsked first
// thing, which becomes the fake when it sees that argument and the state
// directory in its environment. A test binary run any other way never sees both.
package cadtest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A solid whose id or label contains one of these asks the fake to misbehave.
const (
	// Slow never answers: it sleeps until it is killed.
	Slow = "fake-kernel-slow"
	// Crash exits without answering, every time.
	Crash = "fake-kernel-crash"
	// CrashOnce exits without answering the first time any process of this fake
	// kernel meets it, and answers every time after.
	CrashOnce = "fake-kernel-crash-once"
)

const envDir = "FORGE_CAD_FAKE_KERNEL_DIR"

// envStartDelay holds the ready banner back, so a fence can end a caller's
// deadline while the process is still starting (SlowStart).
const envStartDelay = "FORGE_CAD_FAKE_KERNEL_START_DELAY"

// envBuildDelay holds every answer back, so a fence can queue several builds for
// one process and time how long each waited (SlowBuilds).
const envBuildDelay = "FORGE_CAD_FAKE_KERNEL_BUILD_DELAY"

// RunIfAsked becomes the fake kernel, and exits, when this process was started as
// one. Call it first in TestMain.
func RunIfAsked() {
	dir := os.Getenv(envDir)
	if dir == "" || len(os.Args) != 2 || filepath.Base(os.Args[1]) != "sidecar.py" {
		return
	}
	os.Exit(serve(dir))
}

// FakeKernel returns the "python" to give cad.New for a fake kernel, and the
// directory it records its starts in. The package's TestMain must call RunIfAsked.
func FakeKernel(t *testing.T) (python, dir string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	t.Setenv(envDir, dir)
	return exe, dir
}

// SlowStart makes every fake kernel process started after it wait d before it
// says it is ready — the stand-in for build123d's import, which is seconds.
func SlowStart(t *testing.T, d time.Duration) {
	t.Helper()
	t.Setenv(envStartDelay, d.String())
}

// SlowBuilds makes every fake kernel process started after it answer each build
// only after d — the stand-in for a real build's seconds, so the time a build
// spends waiting for a process can be told apart from the time it spends in one.
func SlowBuilds(t *testing.T, d time.Duration) {
	t.Helper()
	t.Setenv(envBuildDelay, d.String())
}

// Starts is how many fake kernel processes have started so far.
func Starts(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "starts"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

func serve(dir string) int {
	f, err := os.OpenFile(filepath.Join(dir, "starts"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 2
	}
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()

	if d, err := time.ParseDuration(os.Getenv(envStartDelay)); err == nil {
		time.Sleep(d)
	}
	fmt.Println(`{"ready":true}`)
	buildDelay, _ := time.ParseDuration(os.Getenv(envBuildDelay))
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return 0
		}
		var req struct {
			Solids []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"solids"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Printf("{\"ok\":false,\"error\":%q}\n", err.Error())
			continue
		}
		asks := func(what string) bool {
			for _, s := range req.Solids {
				// CrashOnce contains Crash, so Crash is matched as a whole word.
				if s.ID == what || s.Label == what || strings.HasSuffix(s.ID, "/"+what) {
					return true
				}
			}
			return false
		}
		switch {
		case asks(Slow):
			time.Sleep(10 * time.Minute)
			return 0
		case asks(Crash):
			return 3
		case asks(CrashOnce):
			marker := filepath.Join(dir, "crashed-once")
			if _, err := os.Stat(marker); os.IsNotExist(err) {
				_ = os.WriteFile(marker, nil, 0o600)
				return 3
			}
		}
		time.Sleep(buildDelay)
		fmt.Printf("{\"ok\":true,\"parts\":%d}\n", len(req.Solids))
	}
}

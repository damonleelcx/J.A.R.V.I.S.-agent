//go:build windows

// ctrlrun starts a program in a new process group on this process's console and,
// when a trigger file appears, sends that group CTRL_BREAK_EVENT through
// GenerateConsoleCtrlEvent: the event Go's os/signal delivers as os.Interrupt on
// Windows, so signal.NotifyContext(os.Interrupt, SIGTERM) in the program fires
// exactly as it would for Ctrl-C in a terminal.
//
// Why not taskkill: without /F it posts WM_CLOSE, which a console program without
// a window never receives; with /F it is TerminateProcess, a kill.
//
// Why CTRL_BREAK and not CTRL_C: a process created with CREATE_NEW_PROCESS_GROUP
// has Ctrl-C disabled; Ctrl-Break cannot be disabled and is routed to the group.
//
//	ctrlrun -log worker.log -trigger stop.now -- forge-worker.exe
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procAllocConsole         = kernel32.NewProc("AllocConsole")
	procGenerateConsoleCtrlE = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procSetConsoleCtrlHandlr = kernel32.NewProc("SetConsoleCtrlHandler")
)

const ctrlBreakEvent = 1

func stamp() string { return time.Now().Format("15:04:05.000") }

func main() {
	logPath := flag.String("log", "", "file the program's stdout and stderr are appended to")
	trigger := flag.String("trigger", "", "file whose appearance sends the stop")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 || *logPath == "" || *trigger == "" {
		fmt.Fprintln(os.Stderr, "usage: ctrlrun -log f -trigger f -- program args...")
		os.Exit(2)
	}
	// A console to share with the child. Fails harmlessly when there already is one.
	r, _, e := procAllocConsole.Call()
	fmt.Printf("%s ctrlrun AllocConsole=%d (%v)\n", stamp(), r, e)
	// Ignore the break ourselves: we are in the same console, not the same group,
	// but a handler of nil + TRUE makes this process immune to Ctrl-C either way.
	procSetConsoleCtrlHandlr.Call(0, 1)

	lf, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer lf.Close()
	os.Remove(*trigger)

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pid := cmd.Process.Pid
	fmt.Printf("%s ctrlrun started pid=%d\n", stamp(), pid)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var sent time.Time
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			code := cmd.ProcessState.ExitCode()
			if sent.IsZero() {
				fmt.Printf("%s ctrlrun child exited on its own code=%d err=%v\n", stamp(), code, err)
			} else {
				fmt.Printf("%s ctrlrun child exited code=%d err=%v %s after CTRL_BREAK\n",
					stamp(), code, err, time.Since(sent).Round(time.Millisecond))
			}
			return
		case <-tick.C:
			if !sent.IsZero() {
				continue
			}
			if _, err := os.Stat(*trigger); err == nil {
				r, _, e := procGenerateConsoleCtrlE.Call(ctrlBreakEvent, uintptr(pid))
				sent = time.Now()
				fmt.Printf("%s ctrlrun GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT, %d)=%d (%v)\n", stamp(), pid, r, e)
			}
		}
	}
}

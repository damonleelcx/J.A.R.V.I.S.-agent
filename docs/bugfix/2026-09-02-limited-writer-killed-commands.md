# The output limiter killed the commands it was limiting

**Date:** 2026-09-02 · **Severity:** high — silently truncated *and* prematurely
killed shell commands · **Owner:** tools/shell_run

## Symptom

CI failed on a commit that passed locally:

```
--- FAIL: TestShellOutputIsBounded
    tools_test.go:334: truncated output did not say so
```

Reproducing it locally failed. The same command, the same test, green every
time. That gap was the actual finding.

## Root cause

`limitedWriter` caps how much of a command's output is kept. Its `Write` looked
like this:

```go
if len(p) > remaining {
    p = p[:remaining]        // reslice
}
n, err := l.w.Write(p)
l.written += n
return len(p), err           // ← len(p) is now the CLIPPED length
```

`io.Writer`'s contract: *a write returning `n < len(p)` must return a non-nil
error.* This returned a short count with a nil error. `os/exec`'s output copier
sees the short write, produces `io.ErrShortWrite`, and **kills the command**.

So a limiter written to truncate *output* was truncating the *process*. And
because the command died at that moment, it never produced the later write that
the truncation notice depended on — so the result came back short, complete-looking,
and unmarked.

Two defects, one line apart:

1. **Short write kills the command.** Any command whose output arrived in a
   single chunk larger than the remaining budget.
2. **The truncation notice was emitted from inside `Write`**, on the *next* call
   after the budget ran out. No next call, no notice.

## Why local passed and CI failed

`os/exec` copies pipe output in ~32KB chunks. On this machine the chunks were
always smaller than the 64KB budget, so the reslice branch was reached only when
`remaining` had shrunk below a chunk — leaving a later write to carry the notice,
and never triggering a short write large enough to matter.

CI's buffering differed. That is the whole story: **the bug was in a branch the
local environment rarely entered.**

## Fix

```go
consumed := len(p)           // captured BEFORE any reslicing
...
return consumed, nil         // always report the whole slice as consumed
```

and the notice moved out of `Write` entirely, appended once by the caller after
the command exits, where it cannot depend on the shape of the output.

## Regression fence

`TestLimitedWriterAlwaysReportsTruncation` tests the writer **directly**, across
eight write patterns: one enormous write, one just over, exactly at the boundary,
two writes crossing it, many small ones, and the under-limit and empty cases.

It asserts both halves on every pattern:

- `Write` reports the full slice consumed — *"the command would be killed with
  EPIPE"* is the failure message, because that is what actually happens.
- Clipped output says so; unclipped output does not. A notice that always appears
  teaches the reader to ignore it.

## What this cost, and the lesson

Three attempts, and the first two were wrong in instructive ways:

1. **First fix** moved the notice to after the command exits. Correct, but it
   addressed only the second defect. The short write was still there.
2. **First test** drove `shell_run` with a large command and claimed to
   reproduce the CI failure. It did not — restoring the original implementation
   left it green, because the shell's chunking never produced the failing shape.
   A test that cannot fail against the bug it names is worse than no test: it
   makes the bug look fixed.
3. **Third attempt** dropped the shell entirely and tested the writer. It failed
   immediately, on the first case, with the real defect — which two layers of
   buffering had been hiding.

> When a failure will not reproduce, stop reproducing the *scenario* and test the
> *invariant*. The scenario runs through layers you do not control; the invariant
> does not need them.

The integration test kept its bounded-size assertion and dropped the truncation
assertion, with a comment saying why: whether that particular command overruns
the budget depends on the platform's coreutils and pipe buffering, and an
assertion resting on that is a flaky test rather than a check.

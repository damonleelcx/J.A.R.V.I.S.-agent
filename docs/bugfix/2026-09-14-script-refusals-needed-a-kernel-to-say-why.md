# Script refusals needed a kernel to say why, and main's CI has been red since

**Date:** 2026-09-14 · **Status:** fixed (PR against `main`) · **Severity:** high for CI. `main`'s `check` job has failed
on every push since #49 (2026-09-11), and so has every PR built on `main`.

## Summary

The script sandbox is designed so that its **refusals** (no imports, no files, no network, no dunders) hold and explain
themselves on a machine with Python but **no CAD kernel**. CI's `check` job is such a machine on purpose.

#49 broke that in two ways:

1. **A refusal imported build123d before it could be worded.** To add "did you mean" and signature hints, the refusal
   handler in `script.py` called `namespace(True)`, which imports build123d. Without a kernel that import raised
   `ModuleNotFoundError` *inside* the handler. Every refusal came back as `No module named 'build123d'` instead of
   its reason.
2. **Nine new tests needed a kernel but did not skip without one.** They asked for an interpreter through
   `scriptKernel`, which falls back to a bare `python3` on purpose so that the refusal tests can run anywhere. Tests
   that build, or read a builder's signature, then failed on the check job instead of skipping.

## Symptom

On `main`'s CI (`a8993bb`, and `fab5666` before it), 14 `TestScript_*` tests failed, for example:

```
--- FAIL: TestScript_RefusesTheWayOut/importing_anything_at_all
    script_test.go:104: refused for the wrong reason.
        ModuleNotFoundError: No module named 'build123d'
```

## Impact

- **CI:** `main`'s `check` job has been red since 2026-09-11, so every PR on `main` inherited a red check. That hides
  real failures behind a known one.
- **Product (no kernel installed):** a script that should have been refused with a reason ("`os` is not allowed")
  instead reported a missing module. That wording sends a model's repair loop somewhere useless. The refusal itself
  still held, since nothing was executed. A deployment *with* a kernel was unaffected.

## Preconditions

Python is present and build123d is not. That describes CI's `check` job, and any FORGE deployment whose
`FORGE_CAD_PYTHON` points at an interpreter without the kernel.

## Root cause

**Surface.** `main()` in `internal/domain/cad/script.py`, in `except Refused`:
`ns, _ = namespace(True)`.

**Deeper.** The file had a written rule that #49's new hint code did not follow. `_builder_names` and `namespace` both
say that the refusals must be exercisable where no kernel is installed ("which is where CI runs"). The new handler even
promised that "on a machine with no kernel the namespace is empty and it simply says less". But `namespace(True)` does
not return an empty namespace without a kernel; it raises. The nine new tests used the refusal tests' helper, whose
bare-python3 fallback exists precisely for kernel-less runs.

**Why it was not caught before merge.** Locally the tests ran against `.cadvenv`, which has build123d, so both defects
were invisible. #49 merged while CI's `check` job was red.

**Owner:** #49 (`fab5666`), "Run the scripts it writes, …".

| Evidence | Result |
|---|---|
| Commits between the last green `main` run (`22f5579`) and the first red one (`fab5666`) | exactly one, #49 |
| The whole cad package run as the check job runs it (`FORGE_CAD_KERNEL` = Homebrew `python3.13` without build123d, no `FORGE_CAD_PYTHON`) | fails the **same 14 tests**, name for name |

## Fix

- **`script.py`:** the refusal handler builds its hint namespace with builders only when build123d is installed:
  `namespace(importlib.util.find_spec("build123d") is not None)`. Without a kernel the hint says less, as the comment
  promised. The Why comment links to this document.
- **`script_test.go`:**
  - `scriptKernel`'s interpreter lookup is extracted as `scriptPython`, unchanged.
  - A new `buildingKernel(t)` uses the same lookup and skips when that interpreter cannot import build123d, asked with
    `hasBuild123d` (the one definition of "has a kernel").
  - The nine kernel-needing tests from #49 use `buildingKernel`. The refusal tests keep `scriptKernel`, so they still
    run without a kernel.

## Verification

| Run | Before | After |
|---|---|---|
| cad package with a kernel-less Python (CI's `check` view) | 14 failures | **0** |
| after only the `script.py` change | — | 9 failures (the kernel-needing tests), which is what showed the second defect |
| cad package with the pinned `.cadvenv` (CI's `kernel` view) | — | **0 failures**; the nine run and pass, not skip |

## Regression prevention

| Test | What it holds |
|---|---|
| `TestScript_RefusalsSayWhyWithoutAKernel` | Finds a Python on PATH that cannot import build123d (skips if none) and checks that an import, a name that is not on the list, and a file read are each refused for their reason, never "No module named". |
| `TestScript_ATestThatNeedsTheKernelSkipsWithoutIt` | Pins `FORGE_CAD_KERNEL` to a kernel-less Python and checks that `buildingKernel` skips. It goes red on a laptop that has build123d too. |

Drills in `scripts/drill-fences.sh`:
- **the refusal hint imports the kernel again** puts `namespace(True)` back.
- **a test that needs the kernel runs without it** removes the skip.

## Not in this fix

- **The four older inline guards remain.** Some pre-#49 kernel-needing script tests still skip by looking for
  `No module named 'build123d'` in a script's error. They work, and converting them to `buildingKernel` is a separate
  tidy-up.
- **CI's `kernel` job has a separate open failure on #61.** `TestScript_BuildsWhatTheVocabularyCannot` exceeds the
  30 s script limit on GitHub's x86_64 runner, while the same script takes 2 s here and 3.1 s in the arm64 production
  image capped to one CPU. That is platform-specific and is tracked on #61.

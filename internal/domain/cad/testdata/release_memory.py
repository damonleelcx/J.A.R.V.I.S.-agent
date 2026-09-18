"""The sidecar hands its allocator's free pages back after each reply, for
release_memory_kernel_test.go (kernel last walls).

Usage: release_memory.py <sidecar.py>

Runs the sidecar's own main() loop in this process on four request lines (a build,
an unreadable line, two STEP exports), with _release_memory wrapped so every call is recorded:
how many replies had been written when it ran, and whether the reply it followed was
still referenced (a reply the loop still holds is memory malloc_trim cannot return).
Then asks _malloc_trim / _release_memory directly what this platform has: glibc's
malloc_trim on Linux with glibc, nothing anywhere else — and nothing must raise.

Also counts the documents the XDE application holds before and after: an export's
document must not be left open in the application (see _step_document).

Prints one JSON object.
"""

import importlib.util
import io
import json
import platform
import sys
import weakref


def load(path):
    spec = importlib.util.spec_from_file_location("sidecar", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class Reply(dict):
    """A reply that can be watched: a dict subclass takes a weak reference."""


def main():
    sidecar = load(sys.argv[1])
    box = {"id": "a", "label": "A", "shape": "box", "dims": {"width": 10.0, "height": 10.0, "depth": 10.0},
           "matrix": [1, 0, 0, 0, 1, 0, 0, 0, 1], "position": [0.0, 0.0, 0.0]}
    step = json.dumps({"solids": [box, dict(box, id="b", position=[20.0, 0.0, 0.0])], "format": "step"})
    lines = [json.dumps({"solids": [box], "format": ""}), "{not json", step, step]
    out = io.StringIO()
    calls, watched = [], []
    real_build, real_release = sidecar._build, sidecar._release_memory

    def build(request):
        reply = Reply(real_build(request))
        watched.append(weakref.ref(reply))
        return reply

    def release():
        written = out.getvalue().count("\n")
        calls.append({"replies_written": written - 1,  # less the ready banner
                      "last_reply_still_held": bool(watched) and watched[-1]() is not None,
                      "ran": real_release()})
        return calls[-1]["ran"]

    sidecar._build, sidecar._release_memory = build, release
    app = sidecar.XCAFApp_Application.GetApplication_s()
    documents_before = app.NbDocuments()
    stdin, stdout = sys.stdin, sys.stdout
    sys.stdin, sys.stdout = io.StringIO("\n".join(lines) + "\n"), out
    try:
        sidecar.main()
    finally:
        sys.stdin, sys.stdout = stdin, stdout
        sidecar._build, sidecar._release_memory = real_build, real_release
    documents_after = app.NbDocuments()
    replies = [json.loads(l) for l in out.getvalue().splitlines()[1:]]
    glibc = sys.platform.startswith("linux") and platform.libc_ver()[0] == "glibc"
    trim = sidecar._malloc_trim()
    ran = sidecar._release_memory()
    switch = sidecar._RELEASE_AFTER_REPLY
    sidecar._RELEASE_AFTER_REPLY = False
    ran_switched_off = sidecar._release_memory()
    sidecar._RELEASE_AFTER_REPLY = switch
    json.dump({"platform": sys.platform, "glibc": glibc, "replies": len(replies),
               "replies_ok": [r.get("ok") for r in replies], "calls": calls,
               "steps_written": sum(1 for r in replies if r.get("step")),
               "documents_before": documents_before, "documents_after": documents_after,
               "trim_found": trim is not None, "release_ran": ran, "release_ran_switched_off": ran_switched_off},
              sys.stdout)


if __name__ == "__main__":
    main()

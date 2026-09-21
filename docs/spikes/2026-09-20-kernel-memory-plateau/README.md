# The kernel's ≈15 MiB per export: where it stops (2026-09-20)

PR 152 added `malloc_trim(0)` after every reply and cut the kernel's idle memory
after a 90,880-occurrence STEP export from ~1.35 GiB to ~0.5 GiB. It closed with
one thing unexplained:

> ⚠️ Not explained: with the trim, RSS still rises ≈15 MiB per export over six
> (505 → 583), with glibc heap in-use flat and no Python-tracked growth. Six
> exports do not show whether it levels off.

Six exports cannot tell a settling curve from a leak, and the two have opposite
consequences for a worker pod that is alive for days. This ran it long enough,
and then asked the allocator directly where the pages were going.

**Answer: it is bounded, and the bound is measured. Idle RSS cannot pass
≈1,275 MiB — the memory the same process holds with the trim switched off — and
that is already less than the ≈1,530 MiB peak an export reaches, which is what
sizes the pod. The ≈15 MiB per export was the first nine exports only; the rate
then falls to 0.76 and then to 0.32 MiB per export, and every byte of it is pages
inside a heap whose SIZE does not grow. Nothing is retained, so there is nothing
of ours to fix.**

## 1. It is not 15 MiB per export. That was the first nine.

`long_idle_rss.py`, 200 STEP exports of `barrel-9.json` (90,880 occurrences)
through the sidecar's real `main()` loop, the branch's `sidecar.py` (trim on),
`forge-linux-test` under `--cpus=1 --memory=6g` — PR 152's conditions, 200 exports
instead of 4. `data/head-200-exports.jsonl` (an earlier 80-export run,
`data/head-80-exports.jsonl`, agrees at every point to within 1 MiB).

| after export | idle RSS MiB | rate over the segment |
|---:|---:|---|
| (ready, before any export) | 443.4 | |
| 1 | 482.0 | |
| 5 | 548.2 | |
| 9 | 579.8 | **15.2 MiB/export** over exports 1–9 |
| 10 | 582.8 | |
| 20 | 587.8 | |
| 40 | 604.6 | |
| 60 | 618.5 | |
| 80 | 636.2 | **0.76 MiB/export** over exports 10–80 |
| 100 | 641.2 | |
| 120 | 655.0 | |
| 140 | 651.0 | |
| 160 | 661.8 | |
| 180 | 670.7 | |
| 200 | **674.7** | **0.32 MiB/export** over exports 80–200 |

PR 152's six exports land entirely inside the first segment, which is why they
read as a straight ≈15 MiB per export. They were the settling, not the rate.

The rate falls by a factor of 20 after the first nine exports and then halves
again over the next 120 (0.20 MiB/export over exports 180–200). That is the shape
of something saturating, not of a leak — and sections 2 and 3 say what it is
saturating towards.

## 2. Nothing is retained. The allocator's own numbers, every export.

PR 152's `export_memory.py` reads `mallinfo2()` after each export: `uordblks` is
bytes IN USE, `arena` is what the main heap took with `brk`, `fordblks` is free
bytes the process still holds, `hblkhd` is bytes in `mmap`'d chunks. 40 exports,
`--remedy trim`, same image and limits. `data/mallinfo-40-exports.jsonl`.

| MiB, after the trim | import | export 1 | export 20 | export 40 |
|---|---:|---:|---:|---:|
| **`uordblks` (in use)** | 123.7 | **127.0** | **127.0** | **127.0** |
| `arena` (heap size) | 125.0 | 812.7 | 812.7 | 812.7 |
| `fordblks` (free, held) | 1.2 | 685.7 | 685.7 | 685.7 |
| `hblkhd` (mmap'd chunks) | 4.1 | 34.6 | 34.6 | 34.6 |
| XDE documents open | — | 0 | 0 | 0 |
| objects the cycle collector tracks | — | 395,356 | 395,116 | 395,116 |
| VmRSS | 438.0 | 507.9 | 614.7 | 635.4 |

`uordblks` is **127.0 MiB at every one of the forty exports** — identical to the
byte. The arena does not grow after the first export. No XDE document is held
(PR 152's fix still holding), and Python's object count is flat from export 7.

So: the process is not holding anything it did not hold after export 1. The
resident set grows anyway.

## 3. What is growing, and what stops it

`malloc_trim(0)` returns whole free PAGES. A page holding even one live chunk
cannot be returned, and glibc will not move the chunk to free it. Each export
allocates and frees its way across the same fixed 812.7 MiB arena, and each time
it leaves a few more pages with something live on them. Those pages stay
resident. Nothing is leaked; the same bytes are simply spread over more pages.

That makes the ceiling obvious and measurable: **the whole arena resident**,
which is precisely what the process holds when the trim never runs. So the same
loop was run with `_RELEASE_AFTER_REPLY = False` — PR 152's own switch — for 25
exports. `data/notrim-25-exports.jsonl`.

| after export | idle RSS MiB, trim OFF | idle RSS MiB, trim ON |
|---:|---:|---:|
| (ready) | 443.1 | 443.4 |
| 1 | 1,134.1 | 482.0 |
| 10 | 1,270.3 | 582.8 |
| 15 | 1,266.4 | 583.8 |
| 20 | 1,270.4 | 587.8 |
| 25 | **1,275.3** | 590.8 |
| 200 | — | 674.7 |

Trim off, idle RSS reaches ≈1,270 MiB by export 10 and is flat from there:
1,270.3 / 1,266.4 / 1,270.4 / 1,269.4 / 1,272.4 / 1,272.4 / 1,274.3 / 1,275.3.
That is the arena fully resident, and it is the ceiling the trimmed run is
creeping towards — from 675 MiB at export 200, at a rate that has already fallen
from 15.2 to 0.2 MiB per export and is still falling.

**The bound, stated as a number: idle RSS after an export cannot pass ≈1,275 MiB
on this workload.** Above it there is nothing left to make resident: the heap
does not grow (section 2), and 1,275 MiB is what the process holds with no trim
at all.

## Is it ours?

No, and there is nothing to fix. `uordblks` identical across 40 exports rules out
retention by FORGE, by build123d and by OCCT alike — an object still referenced
anywhere would be bytes in use. `NbDocuments() == 0` rules out a repeat of the
XDE leak PR 152 found. The remaining movement is glibc deciding which of its own
already-allocated pages stay resident, inside a heap of fixed size.

The trim is still worth what PR 152 paid for it: **600 MiB less at export 200**
(675 vs the 1,275 the untrimmed process holds), for 23–29 ms after each reply.

## What this does NOT establish

- **arm64, or the 2 GiB worker pod.** Every number is amd64 under Docker Desktop
  with `--cpus=1 --memory=6g`, not the pod. The pod is sized by the PEAK
  (`VmHWM` reached 1,532 MiB over these 200 exports), which is above the idle
  bound either way.
- **Other workloads.** One document, one format. The arena's size is a function
  of the largest export the process has served; a bigger design would set a
  bigger arena and therefore a higher ceiling.
- **A measured plateau in the trimmed run.** The trimmed curve is still rising at
  export 200, at 0.2 MiB per export. The claim that it stops at ≈1,275 MiB rests
  on the arena being fixed (section 2) and on the untrimmed plateau (section 3),
  not on watching the trimmed one flatten.
- **Timings.** Every round trip here was contended — other builds were running on
  this laptop throughout. Nothing above is a timing claim.

## Reproduce

```sh
export PYTHONUTF8=1
D=<dir with barrel-9.json>      # FORGE_SCALE_MEASURE_OUT=$D FORGE_SCALE_MEASURE_BAYS=9 \
                                #   go test ./internal/domain/geometry -run TestScaleUp_MeasureAirframeBarrel
cp internal/domain/cad/sidecar.py $D/sidecar-head.py
sed 's/_RELEASE_AFTER_REPLY = True/_RELEASE_AFTER_REPLY = False/' $D/sidecar-head.py > $D/sidecar-notrim.py

H=docs/spikes/2026-09-20-kernel-memory-plateau
docker run --rm --cpus=1 --memory=6g -v $H:/h:ro -v $D:/d:ro forge-linux-test \
  python3 /h/long_idle_rss.py /d/sidecar-head.py   /d/barrel-9.json --exports 200
docker run --rm --cpus=1 --memory=6g -v $H:/h:ro -v $D:/d:ro forge-linux-test \
  python3 /h/long_idle_rss.py /d/sidecar-notrim.py /d/barrel-9.json --exports 25
docker run --rm --cpus=1 --memory=6g -v docs/spikes/2026-09-17-kernel-last-walls:/h:ro -v $D:/d:ro \
  forge-linux-test python3 /h/export_memory.py /d/sidecar-head.py /d/barrel-9.json --exports 40 --remedy trim
```

2026-09-20/21, Windows 11, Intel Core i7-12650H (16 logical), 64 GB, Docker
Desktop; container `forge-linux-test` (python:3.13-slim-bookworm, Python 3.13.15,
glibc 2.36, build123d 0.11.1 on cadquery-ocp-novtk 7.9.3.1.1).

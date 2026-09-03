Raw captures. Nothing in the write-up is derived from anything outside this
directory, so every number in it can be recomputed from here.

- `forge-baseline-runs.json` — four runs of the identical prompt through FORGE's
  own `POST /v1/converse`, 2026-09-02, `qwen-plus`.
- `zoo-api-surface.txt` — the ML surface of the CURRENT Zoo API, derived from the
  published OpenAPI document (`analyse.py --zoo-spec`).
- `zoo-run-1/` — the successful live generation. `main.kcl` (the model),
  `bracket.step` (exported, AP242 B-Rep), `bracket.png` (render),
  `reasoning.json` (52 reasoning frames — the evidence for HOW it got its
  figures), `transcript.json` (all 62 frames; the rendered-image byte arrays are
  elided, since they were 91% of the file and duplicated `bracket.png` exactly —
  every other frame is verbatim).
- `zoo-run-2/main.kcl` — a second independent generation, used for the
  repeatability table.
- `auth-refused-transcript.json` — a DIFFERENT, failed run, kept on purpose: it
  is the evidence for the auth finding. It contains no geometry. It was briefly
  filed inside `zoo-run-1/`, which made a refusal look like part of the
  successful run.

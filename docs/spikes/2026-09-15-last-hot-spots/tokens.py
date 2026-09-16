"""Bytes AND tokens for a repair prompt, so the 16 KiB budget is chosen on numbers
(docs/spikes/2026-09-15-last-hot-spots).

maxRepairProblemBytes is 16 KiB because it is "about 4,000 tokens at ~4 bytes a
token" and a quarter of the 64 KiB a build step already sends. #114 recorded that the
token estimate was NOT measured with a tokenizer. This measures it.

‼️ What this can and cannot say. FORGE talks to an OpenAI-compatible endpoint
(internal/llm/openai_compatible.go) whose server-side tokenizer is not available
here, so these are tiktoken's BPEs — o200k_base and cl100k_base — run offline in a
throwaway venv. They are a PROXY, not the deployed tokenizer: they establish the
bytes-per-token that FORGE's own prompt text actually runs at, which is the number
the "~4 bytes a token" assumption rests on, and nothing more.

Usage: <venv with tiktoken> tokens.py <file> [<file> ...]
"""
import json
import os
import sys

import tiktoken

ENCODINGS = ("o200k_base", "cl100k_base")

out = []
encoders = {name: tiktoken.get_encoding(name) for name in ENCODINGS}
for path in sys.argv[1:]:
    with open(path, "rb") as fh:
        raw = fh.read()
    text = raw.decode("utf-8", "replace")
    row = {"file": os.path.basename(path), "bytes": len(raw), "characters": len(text),
           "lines": text.count("\n") + (1 if text and not text.endswith("\n") else 0)}
    for name, enc in encoders.items():
        n = len(enc.encode(text))
        row[name] = n
        row[name + "_bytes_per_token"] = len(raw) / n if n else 0.0
    out.append(row)

for row in out:
    print(json.dumps(row))

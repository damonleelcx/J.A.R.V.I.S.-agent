#!/usr/bin/env bash
# Reproduces every result in this spike.
#
# Reads FORGE_LLM_API_KEY from the repository's .env. Costs a fraction of a cent.
# Requires curl, python3 and ffmpeg.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
OUT="${1:-$(mktemp -d)}"
mkdir -p "$OUT"

# shellcheck disable=SC1091
set -a && . "$REPO_ROOT/.env" && set +a
: "${FORGE_LLM_API_KEY:?FORGE_LLM_API_KEY is not set in $REPO_ROOT/.env}"
BASE="${FORGE_LLM_BASE_URL:-https://dashscope.aliyuncs.com/compatible-mode/v1}"

SENTENCE="Say exactly: Setting the wall thickness to two point five millimetres."

echo "== Result 1a: the OpenAI-style speech route does not exist here =="
code=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $FORGE_LLM_API_KEY" \
        -H 'Content-Type: application/json' \
        -d '{"model":"qwen3-tts-flash","input":"hello","voice":"Cherry"}' \
        "$BASE/audio/speech" || true)
echo "   POST $BASE/audio/speech -> HTTP $code   (expected 404)"

echo "== Result 1b: audio needs STREAMING; the non-streaming call bills for it and returns none =="
python3 - "$FORGE_LLM_API_KEY" "$BASE" "$SENTENCE" <<'PY'
import json, sys, urllib.request
key, base, sentence = sys.argv[1], sys.argv[2], sys.argv[3]
body = json.dumps({"model": "qwen3-omni-flash", "modalities": ["text", "audio"],
                   "audio": {"voice": "Cherry", "format": "wav"},
                   "messages": [{"role": "user", "content": sentence}]}).encode()
req = urllib.request.Request(base + "/chat/completions", data=body,
                             headers={"Authorization": "Bearer " + key,
                                      "Content-Type": "application/json"})
d = json.load(urllib.request.urlopen(req))
msg = d["choices"][0]["message"]
print("   message keys:", sorted(msg.keys()))
print("   audio tokens billed:", d["usage"]["completion_tokens_details"].get("audio_tokens"))
print("   audio returned:", "audio" in msg)
PY

echo "== Result 1c + 2: latency, and the format that comes back =="
python3 - "$FORGE_LLM_API_KEY" "$BASE" "$SENTENCE" "$OUT/tts.pcm" <<'PY'
import base64, json, sys, time, urllib.request
key, base, sentence, out = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
body = json.dumps({"model": "qwen3-omni-flash", "modalities": ["text", "audio"],
                   "audio": {"voice": "Cherry", "format": "wav"}, "stream": True,
                   "messages": [{"role": "user", "content": sentence}]}).encode()
for trial in range(3):
    req = urllib.request.Request(base + "/chat/completions", data=body,
                                 headers={"Authorization": "Bearer " + key,
                                          "Content-Type": "application/json"})
    t0, first, raw = time.time(), None, b""
    with urllib.request.urlopen(req) as r:
        for line in r:
            if not line.startswith(b"data: "):
                continue
            p = line[6:].strip()
            if p == b"[DONE]":
                break
            try:
                d = json.loads(p)
            except ValueError:
                continue
            for ch in d.get("choices", []):
                a = ch.get("delta", {}).get("audio")
                if a and a.get("data"):
                    if first is None:
                        first = time.time() - t0
                    raw += base64.b64decode(a["data"])
    print("   trial %d: first audio %.2fs, %.2fs of speech, complete at %.2fs"
          % (trial + 1, first, len(raw) / 2 / 24000, time.time() - t0))
open(out, "wb").write(raw)
print("   first 8 bytes: %r  (raw 16-bit PCM, no container)" % raw[:8])
PY

echo "== Result 3: G.711 preserves the decimal =="
ffmpeg -loglevel error -y -f s16le -ar 24000 -ac 1 -i "$OUT/tts.pcm" "$OUT/full.wav"
ffmpeg -loglevel error -y -i "$OUT/full.wav" -ar 8000 -ac 1 -c:a pcm_mulaw -f wav "$OUT/ulaw.wav"

transcribe() {
  python3 - "$(base64 -i "$1" | tr -d '\n')" <<'PY' > "$OUT/req.json"
import json, sys
print(json.dumps({"model": "qwen3-asr-flash-2026-02-10", "messages": [
    {"role": "system", "content": [{"type": "text", "text":
        "Engineering design review. Transcribe exactly what is said. Write spoken "
        "decimals with the word point preserved: 'two point five' must not become 'two five'."}]},
    {"role": "user", "content": [{"type": "input_audio",
        "input_audio": {"data": "data:audio/ogg;base64," + sys.argv[1]}}]}]}))
PY
  curl -sS -H "Authorization: Bearer $FORGE_LLM_API_KEY" -H 'Content-Type: application/json' \
       -d @"$OUT/req.json" "$BASE/chat/completions" \
    | python3 -c "import json,sys; print('     ', json.load(sys.stdin)['choices'][0]['message']['content'])"
}

for variant in full ulaw; do
  ffmpeg -loglevel error -y -i "$OUT/$variant.wav" -c:a libopus -b:a 16k -ar 16000 -ac 1 "$OUT/$variant.ogg"
  echo "   [$variant]"
  transcribe "$OUT/$variant.ogg"
done

echo
echo "Artifacts in $OUT"

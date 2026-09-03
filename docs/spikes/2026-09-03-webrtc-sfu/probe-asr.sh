#!/usr/bin/env bash
# Reproduces Results 2, 3 and 4 of this spike.
#
# Generates known speech with macOS `say` — so the ground truth is exact rather
# than a human's recollection of what they said — and runs it through the
# configured provider under four system-context conditions.
#
# Reads FORGE_LLM_API_KEY from the repository's .env. Costs a few fractions of a
# cent. Requires macOS (say, afconvert), curl, python3.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
OUT="${1:-$(mktemp -d)}"
mkdir -p "$OUT"

# shellcheck disable=SC1091
set -a && . "$REPO_ROOT/.env" && set +a
: "${FORGE_LLM_API_KEY:?FORGE_LLM_API_KEY is not set in $REPO_ROOT/.env}"
BASE="${FORGE_LLM_BASE_URL:-https://dashscope.aliyuncs.com/compatible-mode/v1}"
MODEL="qwen3-asr-flash-2026-02-10"

VOCAB="Mechanical engineering CAD review. Vocabulary: bracket, wall thickness, tolerance, millimetres, fillet, chamfer, part AK73."
DECIMAL="Transcribe engineering speech. Write spoken decimals with the word point preserved: 'two point five' must not become 'two five'. Preserve units and tolerances exactly."

utter() { # $1=name $2=text
  say -v Samantha -o "$OUT/$1.aiff" "$2"
  afconvert -f WAVE -d LEI16@16000 -c 1 "$OUT/$1.aiff" "$OUT/$1.wav"
}

ask() { # $1=wav $2=system-context -> transcript on stdout
  python3 - "$(base64 -i "$1" | tr -d '\n')" "$2" "$MODEL" <<'PY' > "$OUT/req.json"
import json, sys
b64, ctx, model = sys.argv[1], sys.argv[2], sys.argv[3]
print(json.dumps({"model": model, "messages": [
    {"role": "system", "content": [{"type": "text", "text": ctx}]},
    {"role": "user", "content": [
        {"type": "input_audio", "input_audio": {"data": "data:audio/wav;base64," + b64}}]}]}))
PY
  curl -sS -H "Authorization: Bearer $FORGE_LLM_API_KEY" -H "Content-Type: application/json" \
       -d @"$OUT/req.json" "$BASE/chat/completions" \
    | python3 -c "import json,sys; print(json.load(sys.stdin)['choices'][0]['message']['content'])"
}

echo "== Result 2: the OpenAI-style transcription route does not exist here =="
code=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $FORGE_LLM_API_KEY" \
        -F "file=@/dev/null" -F "model=$MODEL" "$BASE/audio/transcriptions" || true)
echo "   POST $BASE/audio/transcriptions -> HTTP $code   (expected 404)"

echo "== Result 4: latency on an SFU-sized chunk =="
utter short "Increase the fillet to one point five millimetres."
python3 -c "import wave; w=wave.open('$OUT/short.wav'); print('   chunk: %.2fs' % (w.getnframes()/w.getframerate()))"
for i in 1 2 3; do
  /usr/bin/time -p sh -c "$(declare -f ask); OUT='$OUT'; MODEL='$MODEL'; BASE='$BASE'; \
    FORGE_LLM_API_KEY='$FORGE_LLM_API_KEY'; ask '$OUT/short.wav' '' >/dev/null" 2>&1 \
    | awk '/^real/ {printf "   run%s  %ss\n", "'"$i"'", $2}'
done

echo "== Result 3: the decimal matrix (5 trials per cell) =="
utter long "Set the bracket wall thickness to two point five millimetres, tolerance plus or minus zero point one, on part A K seven three."
for wav in long short; do
  echo "-- utterance: $wav --"
  for cond in "empty::" "vocabulary::$VOCAB" "decimal::$DECIMAL" "vocabulary+decimal::$VOCAB $DECIMAL"; do
    label="${cond%%::*}"; ctx="${cond#*::}"
    echo "   [$label]"
    for _ in 1 2 3 4 5; do echo "      $(ask "$OUT/$wav.wav" "$ctx")"; done
  done
done

echo
echo "Artifacts in $OUT"

# A minimal streamed call shaped like the workbench's (stream, include_usage, json_object,
# enable_thinking=false) to see where qwen3.7-plus puts its reply. Reads the key from the
# environment; prints delta field names, the assembled content and usage, never the key.
#   python probe.py <model> [thinking:0|1] [json:0|1]
import json, os, sys, urllib.request
model = sys.argv[1]
thinking = len(sys.argv) > 2 and sys.argv[2] == "1"
jsonmode = not (len(sys.argv) > 3 and sys.argv[3] == "0")
body = {
    "model": model, "stream": True, "max_tokens": 200,
    "stream_options": {"include_usage": True},
    "enable_thinking": thinking,
    "messages": [
        {"role": "system", "content": "Reply ONLY with a JSON object with two string keys: \"speech\" (one short sentence) and \"detail\" (one short sentence)."},
        {"role": "user", "content": "Say hello and name one part of a desk lamp."},
    ],
}
if jsonmode:
    body["response_format"] = {"type": "json_object"}
req = urllib.request.Request(os.environ["FORGE_LLM_BASE_URL"] + "/chat/completions",
    data=json.dumps(body).encode(), method="POST",
    headers={"Authorization": "Bearer " + os.environ["FORGE_LLM_API_KEY"], "Content-Type": "application/json"})
fields, content, reasoning, usage, frames = set(), [], [], None, 0
try:
    with urllib.request.urlopen(req, timeout=120) as r:
        for raw in r:
            line = raw.decode("utf-8").strip()
            if not line.startswith("data: "):
                continue
            data = line[6:]
            if data == "[DONE]":
                break
            frames += 1
            f = json.loads(data)
            if f.get("usage"):
                usage = f["usage"]
            for ch in f.get("choices") or []:
                d = ch.get("delta") or {}
                fields |= set(d.keys())
                if d.get("content"):
                    content.append(d["content"])
                if d.get("reasoning_content"):
                    reasoning.append(d["reasoning_content"])
except urllib.error.HTTPError as e:
    print("HTTP", e.code, e.read()[:400].decode("utf-8", "replace"))
    sys.exit(1)
print("model", model, "thinking", thinking, "json", jsonmode, "frames", frames)
print("delta fields", sorted(fields))
print("content", repr("".join(content))[:600])
print("reasoning chars", len("".join(reasoning)))
print("usage", usage)

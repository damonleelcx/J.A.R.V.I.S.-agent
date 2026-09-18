"""Run 2: respec run 1's car over HTTP (no model is asked) and keep both versions."""
import json, os, sys, time, urllib.request, urllib.error
L = os.path.dirname(os.path.abspath(__file__))
API = "http://127.0.0.1:18480"
p = json.load(open(os.path.join(L, "principals.json"), encoding="utf-8"))
tok = p["owner"]["token"]
src = p["designs"]["car-run1.json"]
params = json.loads(sys.argv[1])


def http(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(API + path, data=data, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + tok)
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            b, code = r.read(), r.status
    except urllib.error.HTTPError as e:
        b, code = e.read(), e.code
    return code, json.loads(b or b"null"), time.time() - t0


code, before, _ = http("GET", "/v1/geometry/%s" % src)
print("GET source", code)
json.dump(before, open(os.path.join(L, "respec-before.json"), "w", encoding="utf-8"), indent=1)
code, r, took = http("POST", "/v1/geometry/%s/respec" % src, {"parameters": params})
print("POST respec", code, "in %.2fs" % took)
if code != 201:
    print(json.dumps(r)[:1500]); sys.exit(1)
v = r["variant"]
print("new version", v.get("version_id"), "v%s" % v.get("version"), "artifact same:",
      v.get("artifact_id") == before["variant"].get("artifact_id"), "caveats", len(r.get("caveats") or []))
for c in r.get("caveats") or []:
    print("  caveat:", json.dumps(c)[:300])
code, after, _ = http("GET", "/v1/geometry/%s" % v["version_id"])
print("GET respecified", code)
json.dump(after, open(os.path.join(L, "respec-after.json"), "w", encoding="utf-8"), indent=1)

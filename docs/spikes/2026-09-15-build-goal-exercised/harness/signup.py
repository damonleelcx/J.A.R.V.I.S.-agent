# Sign a local test account up and in over the API. Prints only ids and statuses;
# the password stays in its file and the token goes to signin.json.
#   python signup.py <base url> <email> <display name> <out dir>
import json, sys, urllib.request, urllib.error, os

base, email, name, out = sys.argv[1:5]
pw = open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "password.txt")).read().strip()

def post(path, body):
    req = urllib.request.Request(base + path, data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"}, method="POST")
    try:
        with urllib.request.urlopen(req) as r:
            return r.status, json.loads(r.read() or b"{}")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b"{}")

st, b = post("/v1/auth/sign-up", {"email": email, "password": pw, "display_name": name})
print("sign-up", st, (b.get("user") or {}).get("id"), (b.get("error") or {}).get("code"))
st, b = post("/v1/auth/sign-in", {"email": email, "password": pw})
print("sign-in", st, (b.get("user") or {}).get("id"), (b.get("error") or {}).get("code"))
if st == 200:
    with open(os.path.join(out, "signin.json"), "w") as f:
        json.dump({"token": b["token"], "user": b["user"]["id"]}, f)

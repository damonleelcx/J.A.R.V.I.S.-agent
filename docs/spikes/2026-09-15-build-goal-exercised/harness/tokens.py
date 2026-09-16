# Sum prompt+completion tokens of forged's forge.llm.* completion lines read on stdin,
# EXCLUDING role=planner: planning a goal is charged to that goal's tokens_spent,
# which watch.sh already counts. Prints one integer.
#   --detail prints per-role sums instead.
import re, sys
from collections import Counter
by = Counter()
for line in sys.stdin:
    role = re.search(r"\brole=(\S+)", line)
    p = re.search(r"\bprompt_tokens=(\d+)", line)
    c = re.search(r"\bcompletion_tokens=(\d+)", line)
    if not (p and c):
        continue
    by[role.group(1) if role else "?"] += int(p.group(1)) + int(c.group(1))
if "--detail" in sys.argv:
    for k, v in sorted(by.items()):
        print(k, v)
else:
    print(sum(v for k, v in by.items() if k != "planner"))

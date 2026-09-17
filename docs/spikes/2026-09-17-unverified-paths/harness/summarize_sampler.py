import sys, time
src, dst = sys.argv[1], sys.argv[2]
rows = {}
for line in open(src, encoding="utf-8", errors="replace"):
    s = line.split()
    if len(s) < 3:
        continue
    try:
        t = int(float(s[1]))
    except ValueError:
        continue
    r = rows.setdefault(t, {"kernel": 0, "worker": 0, "cgroup": 0})
    if s[0] == "P" and len(s) >= 6:
        k = "kernel" if s[3].startswith("python") else "worker"
        r[k] = max(r[k], int(s[4]) // 1024)
    elif s[0] == "C" and s[2].isdigit():
        r["cgroup"] = max(r["cgroup"], int(s[2]) >> 20)
with open(dst, "w", newline="\n") as f:
    f.write("utc,kernel_rss_mib,worker_rss_mib,cgroup_memory_current_mib\n")
    for t in sorted(rows):
        r = rows[t]
        f.write("%s,%d,%d,%d\n" % (time.strftime("%H:%M:%S", time.gmtime(t)), r["kernel"], r["worker"], r["cgroup"]))
print(len(rows), "seconds")

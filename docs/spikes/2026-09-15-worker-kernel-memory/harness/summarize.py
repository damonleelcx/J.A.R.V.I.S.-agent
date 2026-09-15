import csv, glob, os, sys

MiB = 1024 * 1024
d = sys.argv[1] if len(sys.argv) > 1 else os.path.dirname(__file__)
print(f"{'run':<16}{'samples':>8}{'peak WS':>10}{'peak priv':>11}{'py WS':>9}{'py priv':>9}{'go WS':>8}{'procs':>6}{'cpu%':>6}")
for f in sorted(glob.glob(os.path.join(d, "*.csv"))):
    rows = list(csv.DictReader(open(f)))
    if not rows:
        continue
    g = lambda k: max(int(r[k]) for r in rows)
    loads = [float(r["cpu_load"] or 0) for r in rows]
    print(f"{os.path.basename(f)[:-4]:<16}{len(rows):>8}{g('ws_total')/MiB:>9.0f}M{g('priv_total')/MiB:>10.0f}M"
          f"{g('ws_python')/MiB:>8.0f}M{g('priv_python')/MiB:>8.0f}M{g('ws_root')/MiB:>7.0f}M{g('procs'):>6}{sum(loads)/len(loads):>6.0f}")

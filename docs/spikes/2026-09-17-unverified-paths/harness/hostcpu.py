import psutil, time
end = time.time() + 4 * 3600
with open("C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/unverified/data/hostcpu.txt", "a") as f:
    while time.time() < end:
        c = psutil.cpu_percent(interval=5)
        f.write("%.0f %.1f\n" % (time.time(), c))
        f.flush()

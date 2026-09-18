"""Copy what the live re-check kept into the spike's data/, scrubbed of session tokens."""
import json, os, subprocess
L = os.path.dirname(os.path.abspath(__file__))
W = r'C:\Users\damon\Downloads\agents\J.A.R.V.I.S.-agent\.claude\worktrees\agent-a8a11870f431a16f2'
SP = os.path.join(W, 'docs', 'spikes', '2026-09-17-live-findings-fixed')
tok = json.load(open(os.path.join(L, 'principals.json'), encoding='utf-8'))
secrets = [tok[k]['token'] for k in ('owner', 'viewer', 'stranger')]


def put(rel, text):
    for s in secrets:
        text = text.replace(s, '<session token>')
    path = os.path.join(SP, rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    open(path, 'w', encoding='utf-8', newline='\n').write(text)


def read(name):
    return open(os.path.join(L, name), encoding='utf-8', errors='replace').read()


put('data/driver.log', read('run3.log'))
for n in ('run3-summary.json', 'run3-goal.json', 'run3-progress.jsonl'):
    put('data/' + n, read(n))
keep = ('forge.llm.completed', 'forge.task.', 'forge.goal.', 'forge.budget', 'forge.worker.', 'forge.artifact.')
for name in ('worker.log', 'forged.log'):
    out = []
    for l in open(os.path.join(L, name), encoding='utf-8', errors='replace'):
        try:
            e = json.loads(l)
        except ValueError:
            continue
        ev = e.get('event') or e.get('msg') or ''
        if any(ev.startswith(k) for k in keep):
            out.append(l.rstrip('\n'))
    put('data/' + name, '\n'.join(out) + '\n')
q = ("select json_agg(e order by e.seq) from (select seq, kind, actor, summary, payload from forge_livefix.forge_events) e")
ev = subprocess.run(['docker', 'exec', 'forge-pg', 'psql', '-U', 'forge', '-d', 'forge', '-At', '-c', q],
                    capture_output=True, text=True, encoding='utf-8').stdout
put('data/timeline.json', json.dumps(json.loads(ev), indent=1, ensure_ascii=False) + '\n')
q = ("select json_agg(t order by t.created_at) from (select title, status, attempt_count, error_code, error_detail, result, "
     "created_at from forge_livefix.forge_tasks) t")
ts = subprocess.run(['docker', 'exec', 'forge-pg', 'psql', '-U', 'forge', '-d', 'forge', '-At', '-c', q],
                    capture_output=True, text=True, encoding='utf-8').stdout
put('data/tasks.json', json.dumps(json.loads(ts), indent=1, ensure_ascii=False) + '\n')
q = "select tokens_spent, largest_call_tokens, max_tokens, status, failure_code from forge_livefix.forge_goals"
print(subprocess.run(['docker', 'exec', 'forge-pg', 'psql', '-U', 'forge', '-d', 'forge', '-At', '-c', q],
                     capture_output=True, text=True).stdout)
print('ok')

"""A car with seven sub-assemblies for the V4 'looked closely at X of Y' check: the
2026-09-15 verified car (4 sub-assemblies) with run 1's three placed beside it. Only
ids are prefixed; nothing inside a sub-assembly is changed."""
import copy, json
W = r'C:\Users\damon\Downloads\agents\J.A.R.V.I.S.-agent\.claude\worktrees\agent-a325f3417a864b26b'
L = r'C:\Users\damon\AppData\Local\Temp\claude\C--Users-damon-Downloads-agents\9c73bcba-7500-4641-9433-f7c8efd05843\scratchpad\lv'
v = json.load(open(W + r'\docs\spikes\2026-09-15-car-verified\data\car.json', encoding='utf8'))
r = json.load(open(L + r'\car-run1.json', encoding='utf8'))
take = ['front-suspension', 'rear-suspension', 'powertrain']
asms = {a['id']: a for a in r['assemblies']}
defs = {d['id']: d for d in r['definitions']}
need = set()
for a in take:
    for c in asms[a].get('children') or []:
        need.add(c['ref'])
P = 'r1-'
for d in sorted(need):
    nd = copy.deepcopy(defs[d]); nd['id'] = P + d
    for k in ('size_from', 'position_from'):
        nd.pop(k, None)  # run 1's parameters are not in this document
    v['definitions'].append(nd)
for a in take:
    na = copy.deepcopy(asms[a]); na['id'] = P + a
    for c in na.get('children') or []:
        c['ref'] = P + c['ref']
        c.pop('position_from', None)
    for i in na.get('interfaces') or []:
        i.pop('position_from', None)
    v['assemblies'].append(na)
root = next(a for a in v['assemblies'] if a['id'] == v['root'])
for n, a in enumerate(take):
    root['children'].append({'id': P + a + '-beside', 'ref': P + a, 'position': [-1500 + 1500 * n, 0, 3000]})
v['name'] = (v.get('name') or 'car') + ' + run 1 sub-assemblies'
json.dump(v, open(L + r'\car-merged.json', 'w', encoding='utf8'), indent=1)
print('assemblies', len(v['assemblies']), 'definitions', len(v['definitions']), 'root children', len(root['children']))

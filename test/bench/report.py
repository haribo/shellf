#!/usr/bin/env python3
"""Turn results.csv into the two things that matter: the median no-op run, and the slope.

A single ratio ("N times faster") is not reportable on its own — it depends entirely on
the instruction count. The per-instruction cost is what generalises, so it is printed
alongside, derived by least squares over the measured Ns.
"""
import sys, csv, statistics
from collections import defaultdict

rows = list(csv.DictReader(open(sys.argv[1])))
d = defaultdict(list)
for r in rows:
    d[(int(r["n"]), int(r["instructions"]), r["tool"], r["phase"])].append(int(r["ms"]))

ns = sorted({k[0] for k in d})
print(f"\n{'N':>5} {'instr':>6} {'phase':>6} {'shellf ms':>10} {'ansible ms':>11} {'ratio':>7}")
print("-" * 50)
for n in ns:
    instr = next(k[1] for k in d if k[0] == n)
    for phase in ("cold", "noop"):
        s = statistics.median(d.get((n, instr, "shellf", phase), [0]))
        a = statistics.median(d.get((n, instr, "ansible", phase), [0]))
        ratio = f"{a/s:.1f}x" if s else "-"
        print(f"{n:>5} {instr:>6} {phase:>6} {s:>10.0f} {a:>11.0f} {ratio:>7}")

def slope(tool, phase):
    xs, ys = [], []
    for n in ns:
        instr = next(k[1] for k in d if k[0] == n)
        v = d.get((n, instr, tool, phase))
        if v:
            xs.append(instr); ys.append(statistics.median(v))
    if len(xs) < 2:
        return None, None
    mx, my = statistics.mean(xs), statistics.mean(ys)
    num = sum((x - mx) * (y - my) for x, y in zip(xs, ys))
    den = sum((x - mx) ** 2 for x in xs)
    m = num / den
    return m, my - m * mx

print("\nfitted cost model (ms) = fixed + per_instruction x N")
for phase in ("cold", "noop"):
    for tool in ("shellf", "ansible"):
        m, b = slope(tool, phase)
        if m is not None:
            print(f"  {phase:>4} {tool:>8}: fixed={b:8.0f}  per_instruction={m:7.1f}")

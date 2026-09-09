"""Generate builders.txt: the build123d names a script may use.

Run with: make cad-builders

The rule is the SAME one namespace() applies in script.py — build123d's own
classes and functions, never a module, minus its own file access. The list is
checked in so a machine without build123d still refuses `open` and `urlopen`,
and TestScript_TheManifestMatchesTheLibrary keeps the two in step where a kernel
does exist.
"""

import inspect

import build123d as b

DENY = {"available_fonts", "FontManager", "brep_from_stl", "RWStl", "StlAPI_Writer",
        "ExportSVG", "export_to_pcbway", "svgpathtools"}


def denied(n):
    return n in DENY or n.startswith("import_") or n.startswith("export_")


names = []
for n in dir(b):
    if n.startswith("_") or denied(n):
        continue
    v = getattr(b, n)
    if inspect.ismodule(v):
        continue
    # build123d's OWN, not what it re-exports — this is what keeps ctypes out.
    if not str(getattr(v, "__module__", "") or "").startswith("build123d"):
        continue
    names.append(n)

print("# build123d names a script may use. GENERATED — see script.py.")
print("# Regenerate with: make cad-builders")
for n in sorted(names):
    print(n)

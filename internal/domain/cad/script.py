"""Run a build123d script the model wrote, and hand back a solid.

# Why this exists

One JSON object means one part, so a wire wheel's sixty spokes were sixty
hand-written objects. `repeat` covers repetition; this covers everything else a
loop can express — a gear's involute teeth computed from a formula, a spiral, a
lattice, a profile sampled from a curve. The vocabulary cannot say those and
build123d can.

# What this is NOT

It is not a general Python runtime and must never become one. It runs in a
SEPARATE, SHORT-LIVED process — not the long-running kernel — so a script that
hangs, allocates forever or segfaults OCCT takes nothing with it.

# The two layers, and why one is not enough

1. The AST is checked BEFORE anything runs, and anything not on the list is
   refused by name. Import, exec, eval, open, attribute access to dunders,
   comprehension tricks, decorators, global/nonlocal, lambda-in-default —
   rejected at parse time, where rejection is total.

2. The process is stripped: no environment (this deployment holds a database URL
   and a provider key in its own), a temporary working directory, CPU and
   address-space limits, and stdin closed.

A restricted-builtins sandbox ALONE is not a sandbox in CPython — ().__class__.
__bases__[0].__subclasses__() walks out of it in one line, which is exactly why
layer 1 refuses every dunder attribute rather than trying to blacklist the
escape. Layer 2 is what stands if layer 1 is ever wrong.
"""

import ast
import json
import os
import resource
import sys
import tempfile
import traceback

# What a script may name. Everything else is refused, including anything this
# file imports for its own use: the script's namespace is built from this list,
# not from globals().
ALLOWED_BUILDERS = [
    "Box", "Cylinder", "Cone", "Sphere", "Torus", "Wedge",
    "BuildPart", "BuildSketch", "BuildLine",
    "Line", "Polyline", "Spline", "Bezier", "RadiusArc", "CenterArc",
    "Rectangle", "Circle", "Ellipse", "Polygon", "RegularPolygon", "Text",
    "extrude", "revolve", "loft", "sweep", "make_face", "make_hull",
    "fillet", "chamfer", "offset", "mirror", "split", "scale",
    "Plane", "Axis", "Location", "Locations", "PolarLocations", "GridLocations",
    "Rot", "Pos", "Vector", "Compound", "Part", "Sketch", "Curve",
    "Mode", "Align", "Kind", "Side", "Keep", "SortBy", "GeomType", "Select",
    "add", "section", "project", "trace",
]
ALLOWED_MATH = ["pi", "tau", "e", "sin", "cos", "tan", "asin", "acos", "atan",
                "atan2", "sqrt", "hypot", "radians", "degrees", "floor", "ceil",
                "exp", "log", "pow", "fabs"]
ALLOWED_BUILTINS = ["abs", "min", "max", "sum", "len", "range", "round", "enumerate",
                    "zip", "sorted", "reversed", "list", "tuple", "dict", "set",
                    "float", "int", "str", "bool", "print", "all", "any", "divmod"]

# Statements a script may use. Anything absent is refused rather than ignored:
# a script whose import was silently dropped would run and build the wrong thing.
ALLOWED_NODES = (
    ast.Module, ast.Expr, ast.Assign, ast.AugAssign, ast.AnnAssign,
    ast.For, ast.While, ast.If, ast.Break, ast.Continue, ast.Pass,
    ast.With, ast.withitem, ast.Return, ast.FunctionDef, ast.arguments, ast.arg,
    ast.Call, ast.Name, ast.Load, ast.Store, ast.Del, ast.Constant,
    ast.List, ast.Tuple, ast.Dict, ast.Set, ast.Subscript, ast.Slice, ast.Index,
    ast.BinOp, ast.UnaryOp, ast.BoolOp, ast.Compare, ast.IfExp,
    ast.Add, ast.Sub, ast.Mult, ast.Div, ast.FloorDiv, ast.Mod, ast.Pow,
    ast.USub, ast.UAdd, ast.Not, ast.And, ast.Or, ast.Invert,
    ast.Eq, ast.NotEq, ast.Lt, ast.LtE, ast.Gt, ast.GtE, ast.In, ast.NotIn,
    ast.Is, ast.IsNot, ast.BitAnd, ast.BitOr, ast.BitXor, ast.LShift, ast.RShift,
    ast.ListComp, ast.SetComp, ast.DictComp, ast.GeneratorExp, ast.comprehension,
    ast.Attribute, ast.keyword, ast.Starred,
)


class Refused(Exception):
    """A script that will not be run, and why, in words the model can act on."""


def check(source):
    """Refuse anything not on the list, naming what and where."""
    try:
        tree = ast.parse(source, mode="exec")
    except SyntaxError as exc:
        raise Refused("line %s: %s" % (exc.lineno, exc.msg))

    for node in ast.walk(tree):
        if not isinstance(node, ALLOWED_NODES):
            raise Refused(
                "line %s: %s is not allowed here. This runs a drawing, not a program: "
                "no imports, no classes, no exceptions, no file or network access."
                % (getattr(node, "lineno", "?"), type(node).__name__))

        # Dunder attributes are the documented way out of a restricted namespace:
        # ().__class__.__bases__[0].__subclasses__() reaches every loaded class in
        # one expression. Refused as a NAME rather than by blacklisting escapes,
        # because the escapes are not enumerable.
        if isinstance(node, ast.Attribute) and node.attr.startswith("__"):
            raise Refused("line %s: attributes beginning with __ are not allowed"
                          % getattr(node, "lineno", "?"))
        if isinstance(node, ast.Name) and node.id.startswith("__"):
            raise Refused("line %s: names beginning with __ are not allowed"
                          % getattr(node, "lineno", "?"))

    # Every NAME a script reads must be one it was given or one it made.
    #
    # Without this, an unavailable builtin is only caught when it runs: `open`
    # parses fine, the whole kernel loads, and a NameError comes back several
    # seconds later saying "open is not defined" — true, but it reads as a bug in
    # FORGE rather than a rule, and it costs a kernel load to say. Refusing at
    # parse time is faster, total, and can name what IS available.
    known = set(ALLOWED_BUILDERS) | set(ALLOWED_MATH) | set(ALLOWED_BUILTINS)
    bound = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Name) and isinstance(node.ctx, (ast.Store, ast.Del)):
            bound.add(node.id)
        elif isinstance(node, ast.FunctionDef):
            bound.add(node.name)
            for a in list(node.args.args) + list(node.args.posonlyargs) + list(node.args.kwonlyargs):
                bound.add(a.arg)
            if node.args.vararg:
                bound.add(node.args.vararg.arg)
            if node.args.kwarg:
                bound.add(node.args.kwarg.arg)
        elif isinstance(node, ast.withitem) and isinstance(node.optional_vars, ast.Name):
            bound.add(node.optional_vars.id)
    for node in ast.walk(tree):
        if isinstance(node, ast.Name) and isinstance(node.ctx, ast.Load):
            if node.id not in known and node.id not in bound:
                raise Refused(
                    "line %s: %s is not available here. This runs a drawing: the names it "
                    "knows are build123d's builders, the maths functions, and plain Python "
                    "values. There is no file, network or system access of any kind."
                    % (getattr(node, "lineno", "?"), node.id))
    return tree


def namespace():
    """The only names a script can see, built from the list and nothing else."""
    import math as _math
    from build123d import __dict__ as _b123d

    ns = {}
    missing = []
    for name in ALLOWED_BUILDERS:
        if name in _b123d:
            ns[name] = _b123d[name]
        else:
            missing.append(name)
    for name in ALLOWED_MATH:
        if hasattr(_math, name):
            ns[name] = getattr(_math, name)
    builtins = {}
    for name in ALLOWED_BUILTINS:
        builtins[name] = __builtins__[name] if isinstance(__builtins__, dict) else getattr(__builtins__, name)
    ns["__builtins__"] = builtins
    return ns, missing


def limits(cpu_seconds, address_space_bytes):
    resource.setrlimit(resource.RLIMIT_CPU, (cpu_seconds, cpu_seconds))
    resource.setrlimit(resource.RLIMIT_AS, (address_space_bytes, address_space_bytes))
    resource.setrlimit(resource.RLIMIT_NOFILE, (64, 64))
    # No core dumps: a crashed OCCT would otherwise write the whole address space
    # to disk on a node with a shared filesystem.
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))


def main():
    request = json.loads(sys.stdin.read())
    source = request.get("source") or ""
    cpu = int(request.get("cpu_seconds") or 10)
    mem = int(request.get("memory_bytes") or 1024 * 1024 * 1024)

    try:
        limits(cpu, mem)
    except Exception as exc:  # noqa: BLE001 — a platform without rlimits still runs
        sys.stderr.write("limits unavailable: %s\n" % exc)

    os.chdir(tempfile.mkdtemp(prefix="forge-script-"))

    try:
        tree = check(source)
    except Refused as exc:
        print(json.dumps({"ok": False, "refused": True, "error": str(exc)}))
        return

    try:
        ns, missing = namespace()
        if missing:
            sys.stderr.write("not in this build123d: %s\n" % ", ".join(missing))
        exec(compile(tree, "<script>", "exec"), ns)  # noqa: S102 — the point of this file
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"ok": False, "error": "%s: %s" % (type(exc).__name__, exc),
                          "trace": traceback.format_exc()[-1500:]}))
        return

    shape = ns.get("result")
    if shape is None:
        print(json.dumps({"ok": False, "error":
              "the script finished without assigning `result`. The last line must set "
              "`result` to the shape you built."}))
        return

    # Handed back as a STEP string, because that is the one format both sides of
    # this already speak and it carries the real surfaces rather than a
    # tessellation of them.
    try:
        from build123d import export_step
        solid = shape.part if hasattr(shape, "part") else shape
        path = "out.step"
        export_step(solid, path)
        with open(path) as fh:
            step = fh.read()
        volume = float(getattr(solid, "volume", 0.0))
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"ok": False, "error":
              "the script produced something that is not a solid: %s: %s" % (type(exc).__name__, exc)}))
        return

    if volume <= 0:
        print(json.dumps({"ok": False, "error":
              "the script produced a shape with no volume. A surface, an empty compound "
              "and a self-intersecting solid all look like this; check that `result` is a "
              "closed body."}))
        return

    print(json.dumps({"ok": True, "step": step, "volume": volume}))


if __name__ == "__main__":
    main()

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

# What a script may name, from build123d.
#
# # Why a RULE and not a hand-written list
#
# The first version listed forty builders by hand. It refused `cylinder` — and
# then a real script from the live site died on it, because a hand-list is a
# guess about what a model will reach for and the guess is always short. A
# correct script failing for no security gain is the worst kind of refusal: it
# teaches nobody anything and it looks like the sandbox is broken.
#
# # Why NOT simply "everything build123d exports"
#
# Because `from build123d import *` re-exports real modules. Its public
# namespace contains ctypes, copy, contextvars and colorsys — and ctypes is a
# direct route to arbitrary memory and arbitrary code. Handing a script the
# whole namespace would have been a hole, and it looked like the obvious
# simplification.
#
# So: build123d's OWN classes and functions, which is what __module__ says, and
# nothing that is a module. Then minus its own file access, listed below,
# because a CAD library legitimately reads and writes files and a script here
# must not.
DENIED_BUILDERS = frozenset({
    "available_fonts", "FontManager", "brep_from_stl", "RWStl", "StlAPI_Writer",
    "ExportSVG", "export_to_pcbway", "svgpathtools",
})


def _is_denied(name):
    """build123d's own file access, refused by rule so a new one is refused too."""
    return (name in DENIED_BUILDERS
            or name.startswith("import_")
            or name.startswith("export_"))


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
    # The child of an Import/ImportFrom. Listed because ast.walk yields it
    # separately from its parent, and the parent is what decides: walk is
    # breadth-first, so a refused import raises before its alias is reached.
    ast.alias,
)


def _builder_names():
    """Every build123d name a script may use, by the rule in namespace()."""
    try:
        import inspect

        import build123d as _b123d
    except Exception:  # noqa: BLE001 — no kernel installed; the rule still holds
        return set()
    out = set()
    for name in dir(_b123d):
        if name.startswith("_") or _is_denied(name):
            continue
        value = getattr(_b123d, name)
        if inspect.ismodule(value):
            continue
        if not str(getattr(value, "__module__", "") or "").startswith("build123d"):
            continue
        out.add(name)
    return out


def _tolerated_import(node):
    """Is this one of the two imports that grant nothing?

    `import math` and `from build123d import *` only. Not `import math as m`,
    not `import sys, math`, not a relative import — those are refused like any
    other, because the tolerance is for two exact lines and not for importing.
    """
    if isinstance(node, ast.Import):
        return all(a.name == "math" and a.asname is None for a in node.names)
    if isinstance(node, ast.ImportFrom):
        return node.module == "build123d" and node.level == 0
    return False


class _DropImports(ast.NodeTransformer):
    """Removes the tolerated imports so they are never EXECUTED.

    Accepting them at parse time is not enough: exec still runs the statement,
    and __import__ is not in the restricted builtins, so a script opening with
    the two most ordinary lines in build123d died with "ImportError: __import__
    not found" — refused in a way that reads as a broken sandbox rather than a
    rule. They are no-ops by construction, both namespaces being populated
    before the script runs, so dropping them is exactly equivalent to running
    them and considerably safer.
    """

    def visit_Import(self, node):  # noqa: N802 — ast's naming, not ours
        return None if _tolerated_import(node) else node

    def visit_ImportFrom(self, node):  # noqa: N802
        return None if _tolerated_import(node) else node


class Refused(Exception):
    """A script that will not be run, and why, in words the model can act on."""


def check(source):
    """Refuse anything not on the list, naming what and where."""
    try:
        tree = ast.parse(source, mode="exec")
    except SyntaxError as exc:
        raise Refused("line %s: %s" % (exc.lineno, exc.msg))

    for node in ast.walk(tree):
        # The two imports that grant NOTHING, tolerated because refusing them
        # refuses correct scripts for no gain.
        #
        # Every build123d example on earth opens with them, and a model writes
        # what it has read. Measured live 2026-09-09: the first script FORGE
        # produced with this path working began
        #
        #     import math
        #     from build123d import *
        #
        # and was refused outright. Both are no-ops here: the builders and the
        # maths functions are already in the namespace before the script runs.
        # So they are ACCEPTED AND DROPPED — never executed — and nothing else
        # is. `import os` is still refused, and so is `from math import *` from
        # a module that is not one of these two.
        if _tolerated_import(node):
            continue
        if isinstance(node, (ast.Import, ast.ImportFrom)):
            raise Refused(
                "line %s: importing %s is not allowed. Everything you need is already "
                "available: build123d's builders and the maths functions are in scope "
                "before your script runs, so no import is needed for them, and nothing "
                "else can be reached from here."
                % (getattr(node, "lineno", "?"),
                   getattr(node, "module", None) or ", ".join(a.name for a in node.names)))
        if not isinstance(node, ALLOWED_NODES):
            raise Refused(
                "line %s: %s is not allowed here. This runs a drawing, not a program: "
                "no classes, no exceptions, no file or network access."
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
    # The SAME rule the namespace uses, so a name that will exist at run time is
    # not refused at parse time and vice versa. Two lists would drift, and the
    # drift would show up as a correct script refused for a name it is given.
    known = _builder_names() | set(ALLOWED_MATH) | set(ALLOWED_BUILTINS) | {"math"}
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
    tree = ast.fix_missing_locations(_DropImports().visit(tree))
    return tree


def namespace(uses_builders):
    """The only names a script can see, built from the list and nothing else.

    build123d is imported ONLY when the script actually names one of its
    builders. Loading OCCT costs seconds, and a script that is pure arithmetic —
    or one about to be stopped by the CPU limit — should not pay for it. It also
    means the refusals and the limits can be exercised on a machine that has no
    kernel installed, which is where CI runs.
    """
    import math as _math

    ns = {}
    # Bound as a name too, because `import math` is tolerated above and a script
    # that writes it goes on to write math.cos. Pure arithmetic: it reaches no
    # file, no process and no network, and its dunders are refused by the AST
    # check like every other attribute beginning with __.
    ns["math"] = _math
    missing = []
    if uses_builders:
        import inspect

        import build123d as _b123d

        for name in dir(_b123d):
            if name.startswith("_") or _is_denied(name):
                continue
            value = getattr(_b123d, name)
            if inspect.ismodule(value):
                continue
            # build123d's OWN, not what it re-exports.
            #
            # Doubled with the ismodule check above ON PURPOSE, and a drill
            # showed why the pair is not redundant theatre: removing EITHER
            # leaves ctypes out (a module has no __module__, and a module is a
            # module), but removing BOTH lets a script reach it — and the only
            # thing left stopping it is that a module is not a solid. That is
            # luck, not a boundary.
            if not str(getattr(value, "__module__", "") or "").startswith("build123d"):
                continue
            ns[name] = value

    # The maths functions, bare, so `cos(x)` works as well as `math.cos(x)`.
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
        uses = {n.id for n in ast.walk(tree)
                if isinstance(n, ast.Name) and isinstance(n.ctx, ast.Load)}
        # Only load OCCT when the script actually names one of its builders:
        # it costs seconds, and the refusals and the limits must be exercisable
        # on a machine with no kernel installed, which is where CI runs.
        ns, missing = namespace(bool(uses & _builder_names()))
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

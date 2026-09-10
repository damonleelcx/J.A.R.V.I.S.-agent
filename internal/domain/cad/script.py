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
import difflib
import json
import os
import re
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
    # Lambda, for the reason FunctionDef is here and by the same argument.
    #
    # ‼️ This is a deliberate widening of the AST whitelist, taken on 2026-09-09
    # with the security decision made explicitly rather than by omission.
    #
    # A lambda's body is a single EXPRESSION, and every node it can contain —
    # Call, Attribute, Subscript, Name, the operators — is already allowed inside
    # a `def`, which this list has always permitted. It introduces no node type
    # that is not reachable without it, binds no name the caller could not bind
    # with an assignment, and reaches no name the known-names rule does not
    # already gate. The escape a restricted namespace actually has is dunder
    # attribute access, and that is refused BY NAME below, inside a lambda body
    # exactly as anywhere else — TestScript_RefusesTheWayOut proves that with a
    # lambda now, not only at the top level.
    #
    # So refusing Lambda while allowing FunctionDef was an inconsistency, not a
    # boundary: it withheld the shorter spelling of something already permitted.
    # It cost real work — on a live run a model wrote
    # `min(range(n), key=lambda i: abs(...))` inside an involute gear and spent a
    # whole repair round being told "Lambda is not allowed here", which reads as
    # a broken sandbox rather than a rule because there is no rule behind it.
    ast.Lambda,
    ast.Call, ast.Name, ast.Load, ast.Store, ast.Del, ast.Constant,
    ast.List, ast.Tuple, ast.Dict, ast.Set, ast.Subscript, ast.Slice, ast.Index,
    ast.BinOp, ast.UnaryOp, ast.BoolOp, ast.Compare, ast.IfExp,
    ast.Add, ast.Sub, ast.Mult, ast.Div, ast.FloorDiv, ast.Mod, ast.Pow,
    # MatMult — the `@` operator — for the reason every operator on this line is
    # here, and by the same argument that admitted Lambda.
    #
    # ‼️ A deliberate widening of the AST whitelist, taken on 2026-09-10 with the
    # security decision made explicitly rather than by omission.
    #
    # EVERY binary operator already on this list dispatches to a dunder method:
    # Add to __add__, Mod to __mod__, Mult to __mul__. MatMult dispatches to
    # __matmul__ and is not different in kind. It binds no name, reaches no
    # module, and cannot produce an object the script could not already hold —
    # the operands are whatever it already has. Writing `x.__matmul__(y)` stays
    # refused, by the dunder-attribute rule, exactly as `x.__add__(y)` is.
    #
    # And it is build123d's OWN idiom, whose sibling was already allowed:
    #
    #     edge @ 0.5   ->  Vector(5, 0, 0)   the point half way along
    #     edge % 0.5   ->  Vector(1, 0, 0)   the tangent there
    #
    # `%` is ast.Mod, three names to the left of this comment, and has worked
    # since the sandbox was written. Refusing the other half of a documented pair
    # was arbitrary, and it cost a real run: a model reached for `@`, was refused,
    # and spent repair attempts on it.
    ast.MatMult,
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
    """Every build123d name a script may use.

    Read from builders.txt, which is GENERATED from the installed library by the
    rule in namespace() and checked in beside this file.

    # Why a file and not the live library

    Deriving the list at parse time from build123d means that on a machine
    WITHOUT build123d there is no list — and then either every builder is
    refused with "Box is not available here" (false: Box is fine, the kernel is
    missing), or the name check is skipped entirely and `open` and `urlopen`
    stop being refused. CI has Python and no kernel, and hit both in turn.

    A checked-in manifest gives the same answer everywhere, so the REFUSALS —
    the part that matters most — hold on a machine that cannot build anything.
    TestScript_TheManifestMatchesTheLibrary keeps it honest where a kernel does
    exist.
    """
    try:
        import inspect

        import build123d as _b123d
    except Exception:  # noqa: BLE001 — no kernel installed
        # None, not an empty set, and the difference is the whole point.
        #
        # An empty set makes the name check refuse EVERY builder with "Box is
        # not available here", which is false and misleading: Box is available,
        # the kernel is not installed. CI has Python and no build123d and saw
        # exactly that. The caller skips the builder-name check when this is
        # None and lets the run fail with the honest ModuleNotFoundError.
        return None
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


def _did_you_mean(name, known):
    """The available names closest to one that does not exist.

    # Why a refusal should name the alternatives

    "Rotate is not available here" is precise about the mistake and gives the
    reader nothing to move toward. The reader here is usually a MODEL being asked
    to correct its own script, and it has no way to enumerate what it may use —
    so it guesses again. Measured live 2026-09-09: four gear requests failed with
    `Rotate is not available here` (the name is `Rotation`, or `Rot`),
    `InvoluteGear is not available here`, and similar. The repair loop spent its
    whole budget re-guessing at an API rather than fixing geometry.

    # Why close matches and not the whole list

    The known set is 253 names. Pasting it into every refusal buries the sentence
    that matters and costs tokens on a path that can run several times a turn. A
    typo or a near-miss is what a suggestion can actually fix, and a name close to
    nothing gets no suggestion at all — which is both the honest answer and the
    one that keeps a refusal about reaching outside the sandbox from reading like
    a spelling correction.

    # Why the cutoff is 0.70 and not difflib's 0.60

    ‼️ Measured against this deployment's own manifest. At 0.60 the refusal for
    `urlopen` — a script trying to reach the network — came back "Did you mean
    len?", because difflib scores 2*3/(7+3) = 0.6 for the three shared letters.
    `socket` suggested `set` and `exec` suggested `Select` on the same rule. At
    0.70 all three suggest nothing, and `Rotate -> Rotation`, the failure this
    exists for, survives at 0.857.

    This is a threshold, not a guarantee: `input` still suggests `int`. That
    costs nothing — `int` IS available, so it discloses nothing and misleads
    nobody — and the sentence after the suggestion is unchanged and still says
    there is no file, network or system access of any kind. What 0.70 buys is
    that the refusals which are ABOUT the boundary do not read as typos.
    """
    # Matched case-INSENSITIVELY, because getting the case wrong is its own
    # common miss — `polyline` for `Polyline`, `BOX` for `Box` — and a
    # case-sensitive comparison scores those no better than a typo. Measured
    # against this manifest: case folding adds `BOX -> Box` and costs nothing,
    # and the names that must suggest NOTHING (urlopen, getattr, socket, exec)
    # still suggest nothing at this cutoff either way.
    #
    # The fold is built from the sorted list so two names differing only in case
    # resolve the same way on every run; the one that sorts first wins, which is
    # arbitrary but stable, and a suggestion is a hint rather than a ruling.
    folded = {}
    for real in sorted(known):
        folded.setdefault(real.lower(), real)

    # ‼️ And the candidate must share the START of the typed name.
    #
    # # Why edit distance alone is not enough
    #
    # A cutoff cannot separate the good suggestions from the bad ones here, and
    # that is measured rather than assumed. Against this manifest:
    #
    #     Rotate     -> Rotation   0.714   the suggestion this feature exists for
    #     module     -> Mode       0.800   nonsense: a gear term and an enum
    #     thickness  -> thicken    0.750   nonsense: a parameter and an operation
    #     input      -> int        0.750   nonsense
    #
    # The one that matters scores LOWER than the three that mislead, so any
    # threshold that keeps it keeps them. `module -> Mode` was live: a model
    # wrote `m = module`, reaching for a gear parameter, and was told to consider
    # build123d's Mode enum.
    #
    # A misspelling preserves the START of a word — `Cylindr`, `polyline`, `BOX`,
    # `sqrtt`, `make_facee` all do — and a wrong word does not. Requiring 70% of
    # the typed name to match as a prefix separates the two sets completely on
    # this manifest, and it drops the junk the cutoff alone let through.
    def shared_prefix(a, b):
        n = 0
        for x, y in zip(a, b):
            if x != y:
                break
            n += 1
        return n

    typed = name.lower()
    near = []
    for candidate in difflib.get_close_matches(typed, sorted(folded), n=5, cutoff=0.70):
        if shared_prefix(typed, candidate) >= 0.70 * len(typed):
            near.append(folded[candidate])
        if len(near) == 3:
            break
    if not near:
        return ""
    return " Did you mean %s?" % ", ".join(near)


def _suggestion_signatures(ns, detail):
    """The signatures of the names a refusal suggested.

    # Why the suggestion alone is not enough

    "Did you mean Rotation, Rot?" fixes the NAME and leaves the call to be
    guessed at, which is the same failure one step later — measured: once the
    names resolved, what remained was
    `BuildSketch.__init__() got an unexpected keyword argument 'local_mode'`.
    Answering both questions in one refusal is one round trip instead of two, and
    the budget for those rounds is small.

    Empty when nothing was suggested, which is the ordinary case for a name that
    was reaching outside the sandbox rather than misspelled.
    """
    if "Did you mean " not in detail:
        return ""
    tail = detail.split("Did you mean ", 1)[1].split("?", 1)[0]
    out = []
    for name in [n.strip() for n in tail.split(",")]:
        if sig := _signature_of(ns, name):
            out.append(sig)
    if not out:
        return ""
    return " They take: " + "; ".join(out) + "."


def _bind_arguments(args, bound):
    """Adds a def's or a lambda's parameter names to the bound set.

    Every kind, because a parameter this misses is a name the script was GIVEN
    and would then be refused as unavailable — a correct script rejected for
    using its own argument.
    """
    for a in list(args.args) + list(args.posonlyargs) + list(args.kwonlyargs):
        bound.add(a.arg)
    if args.vararg:
        bound.add(args.vararg.arg)
    if args.kwarg:
        bound.add(args.kwarg.arg)


# What a refused construct is called in the Python somebody WROTE.
#
# The refusal used to name the AST class — "MatMult is not allowed here" — which
# is the parser's word for it and not anybody's. Measured live: a model reached
# for build123d's own `@` idiom, was told "MatMult", and had to guess what that
# referred to. A message that names a thing the author never typed cannot be
# acted on, and the repair budget is small.
#
# Only the constructs a script plausibly reaches for. Anything absent falls back
# to the class name, which is still better than nothing and is what an exotic
# node deserves.
SYNTAX_NAMES = {
    "MatMult": "the `@` operator",
    "Lambda": "a `lambda`",
    "ClassDef": "a `class`",
    "Try": "`try` / `except`",
    "Raise": "`raise`",
    "Assert": "`assert`",
    "Global": "`global`",
    "Nonlocal": "`nonlocal`",
    "Yield": "`yield`",
    "YieldFrom": "`yield from`",
    "Await": "`await`",
    "AsyncFunctionDef": "an `async def`",
    "AsyncFor": "an `async for`",
    "AsyncWith": "an `async with`",
    "Delete": "`del`",
    "NamedExpr": "the `:=` operator",
    "JoinedStr": "an f-string",
    "FormattedValue": "an f-string",
    "Lambda_": "a `lambda`",
}


def _syntax_name(node):
    """What to call a refused construct, in the words it was written in."""
    kind = type(node).__name__
    return SYNTAX_NAMES.get(kind, kind)


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


def _load_builder_manifest():
    """The generated list, read from beside this file."""
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "builders.txt")
    try:
        with open(path) as fh:
            return frozenset(
                line.strip() for line in fh
                if line.strip() and not line.startswith("#"))
    except Exception:  # noqa: BLE001
        return frozenset()


BUILDER_NAMES = _load_builder_manifest()


class Refused(Exception):
    """A script that will not be run, and why, in words the model can act on."""


def check(source, parameters=None):
    """Refuse anything not on the list, naming what and where.

    `parameters` are the document's own numbers, which are in scope for the
    script — see usable_parameters. They are added to the known names, or a
    script reading a parameter it declared would be refused for using it.
    """
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
                % (getattr(node, "lineno", "?"), _syntax_name(node)))

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
    known = BUILDER_NAMES | set(ALLOWED_MATH) | set(ALLOWED_BUILTINS) | {"math"}
    known |= set(parameters or ())
    bound = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Name) and isinstance(node.ctx, (ast.Store, ast.Del)):
            bound.add(node.id)
        elif isinstance(node, ast.FunctionDef):
            bound.add(node.name)
            _bind_arguments(node.args, bound)
        elif isinstance(node, ast.Lambda):
            # Parameters only: a lambda has no name to bind. Shared with
            # FunctionDef rather than copied, because a parameter kind handled in
            # one and not the other would refuse a correct script for a name it
            # was given — which is the exact failure the comment above this loop
            # warns two lists would produce.
            _bind_arguments(node.args, bound)
        elif isinstance(node, ast.withitem) and isinstance(node.optional_vars, ast.Name):
            bound.add(node.optional_vars.id)
    for node in ast.walk(tree):
        if isinstance(node, ast.Name) and isinstance(node.ctx, ast.Load):
            if node.id not in known and node.id not in bound:
                raise Refused(
                    "line %s: %s is not available here.%s This runs a drawing: the names it "
                    "knows are build123d's builders, the maths functions, this part's own "
                    "parameters, and plain Python values. There is no file, network or "
                    "system access of any kind."
                    % (getattr(node, "lineno", "?"), node.id,
                       _did_you_mean(node.id, known)))
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


def _signature_of(ns, name):
    """The real signature of a builder, from the library that is loaded.

    None when the name is not a builder here, or when it has no introspectable
    signature — six of build123d's names do not, and inventing one for them would
    be worse than saying nothing.
    """
    value = ns.get(name)
    if value is None:
        return None
    # Imported here rather than at the top, matching the two other uses in this
    # file: nothing on the ordinary path needs it, and this one runs only when a
    # script has already failed.
    import inspect

    try:
        return "%s%s" % (name, inspect.signature(value))
    except (TypeError, ValueError):
        return None


def _refused_name(detail):
    """The identifier a refusal is about, or "" when it is not that kind."""
    m = re.search(r"line [^:]*: ([A-Za-z_][A-Za-z0-9_]*) is not available here", detail or "")
    return m.group(1) if m else ""


def _known_names():
    """Every name the checker accepts, for un-saying a suggestion it made."""
    return BUILDER_NAMES | set(ALLOWED_MATH) | set(ALLOWED_BUILTINS) | {"math"}


def _names_in_message(text):
    """The builder names an exception message blames, in the order it blames them.

    Python names the callable in the shapes that matter here:

        BuildSketch.__init__() got an unexpected keyword argument 'local_mode'
        extrude() takes from 0 to 9 positional arguments but 10 were given
        Circle() missing 1 required positional argument: 'radius'
    """
    out = []
    for m in re.finditer(r"\b([A-Za-z_][A-Za-z0-9_]*)(?:\.__init__)?\(\)", text or ""):
        if m.group(1) not in out:
            out.append(m.group(1))
    return out


def _names_on_line(source, lineno):
    """The builders CALLED on one line of the script.

    The traceback says where it went wrong; this says what was being called
    there. It is what covers the errors that name nothing — an OCCT
    Standard_TypeMismatch blames a C++ type and not the Python that reached it,
    and the failing line is the only thing that points back at a builder.
    """
    lines = (source or "").splitlines()
    if lineno is None or lineno < 1 or lineno > len(lines):
        return []
    out = []
    for m in re.finditer(r"\b([A-Za-z_][A-Za-z0-9_]*)\s*\(", lines[lineno - 1]):
        if m.group(1) not in out:
            out.append(m.group(1))
    return out


def _failing_line(exc):
    """The line number in the SCRIPT where it went wrong.

    The deepest frame belonging to "<script>", because the top of the traceback
    is usually somewhere inside build123d and the script's own line is what the
    author can act on.
    """
    tb = exc.__traceback__
    lineno = None
    while tb is not None:
        if tb.tb_frame.f_code.co_filename == "<script>":
            lineno = tb.tb_lineno
        tb = tb.tb_next
    return lineno


# How many signatures one failure is worth. The point is to answer "how is this
# called", not to paste the library: a line calling four builders is answered by
# four, and a script that fails on a line calling twenty is not being helped by
# a wall of text it has to read past.
SIGNATURE_LIMIT = 4


def _context_managers(ns):
    """The names in scope that a `with` statement can actually take."""
    out = []
    for name, value in sorted(ns.items()):
        if name.startswith("_"):
            continue
        if hasattr(type(value), "__enter__") or hasattr(value, "__enter__"):
            out.append(name)
    return out


def _method_hint(ns, name):
    """Whether a refused name is a METHOD on a shape rather than a function.

    # Why this is worth saying

    build123d has both forms and the model reaches for the wrong one. Measured
    live: `rotate` was refused, and the closest global name is `Rotation` — which
    is a Location, not what was wanted, so the suggestion sent the repair
    somewhere useless. `rotate` is real; it is `shape.rotate(...)`.

    Answered from the library itself: every class in scope is asked whether it
    has an attribute of that name. Nothing here maintains a list of methods, so
    nothing here can go stale against the build123d that is installed.
    """
    # ‼️ Reported by where the method is DEFINED, not by which classes have it.
    #
    # The first version listed the classes in scope carrying the attribute, and
    # `rotate` came back as "a method on Airfoil and ArcArcTangentArc and
    # ArcArcTangentLine" — alphabetically-first leaves that inherit it from
    # Shape. True, and useless. __qualname__ names the class that actually
    # defines it, which is the one worth telling somebody about.
    owners = []
    for owner, value in sorted(ns.items()):
        if owner.startswith("_") or not isinstance(value, type):
            continue
        attr = getattr(value, name, None)
        if attr is None or not callable(attr):
            continue
        qual = getattr(attr, "__qualname__", "")
        defined = qual.split(".")[0] if "." in qual else owner
        if defined and defined not in owners:
            owners.append(defined)
    if not owners:
        return ""
    return (" %s is not a function here, but it IS a method on %s — write "
            "shape.%s(...) on the object you built." % (name, " or ".join(sorted(owners)[:2]), name))


def signature_help(ns, source, exc):
    """What the failing call actually takes, appended to the error.

    # Why this is here and not in the caller

    The model that wrote the script has the 209 NAMES it may use and none of
    their signatures, so it guesses — and the guesses were measured: 
    `BuildSketch.__init__() got an unexpected keyword argument 'local_mode'`,
    `Standard_TypeMismatch: TopoDS::Face`. Once the name exists, "not available
    here" cannot help, and the repair loop spends its budget re-guessing at an
    API rather than fixing geometry.

    The signature is produced HERE, at the moment of failure, from the build123d
    that is actually installed. That is the only place it is guaranteed correct:
    a list generated at build time would be a second artifact to keep in step
    with the library, and a wrong signature is worse than none because it is
    read as authoritative.
    """
    # ‼️ The `with` mistake gets a direct answer rather than a signature.
    #
    # Measured live: `with Rotation(...)` produced "'Rotation' object does not
    # support the context manager protocol", and Rotation's signature —
    # `Rotation(*args, **kwargs)` — answers nothing about it. What the model
    # needs is which names a `with` can take, and the library knows.
    if "context manager protocol" in str(exc):
        managers = _context_managers(ns)
        if managers:
            return (" That is not something you can use `with`. The ones you can are: %s."
                    % ", ".join(managers))

    wanted = _names_in_message(str(exc)) + _names_on_line(source, _failing_line(exc))
    out = []
    for name in wanted:
        sig = _signature_of(ns, name)
        if sig and sig not in out:
            out.append(sig)
        if len(out) >= SIGNATURE_LIMIT:
            break
    if not out:
        return ""
    return " The builders on that line take: " + "; ".join(out) + "."


# Python's own keywords, which cannot be names however well-formed they look.
import keyword as _keyword


def usable_parameters(parameters, taken):
    """The document's numbers that may become names, and the ones that may not.

    # Why a script gets these at all

    A scripted part belongs to a document, and that document declares the numbers
    the part is made of — the panel shows them, a person edits them, and every
    other shape in the document is built from them. The script could not see any
    of it. Measured live 2026-09-10, a model asked for a gear wrote

        m = module
        t = teeth_count
        pa = pressure_angle_deg * math.pi / 180
        thick = thickness

    and every one of those was refused as an unavailable name. It is not an
    unreasonable assumption: they ARE the part's parameters. Without them a
    scripted part is the one shape in the document that cannot be parametric,
    which is the opposite of why scripts exist.

    # ‼️ What is refused, and why each one matters

    A parameter name is text from a model, and this puts it straight into the
    namespace a script executes in. So:

      - not an identifier, or a Python keyword: it could not be read anyway, and
        `for` or `class` as a key would simply be dead weight.
      - starting with an underscore: the namespace holds `__builtins__`, and a
        parameter called that would REPLACE the restricted builtins with a float
        — or, worse, if it were ever a dict, with one somebody chose. The dunder
        rule refuses these in source; this refuses them as names.
      - already taken by a builder, a maths function or a builtin: the builder
        WINS. A document with a parameter called `Box` must not turn `Box(...)`
        into a call on a number, because that would let a document change the
        language rather than use it.

    Everything refused is RETURNED rather than dropped, so the runner can say so.
    A parameter that silently is not there is the failure this whole feature
    exists to remove, arriving from the other side.
    """
    usable, refused = {}, []
    for name, value in sorted((parameters or {}).items()):
        if not isinstance(name, str) or not name.isidentifier() or _keyword.iskeyword(name):
            refused.append("%s (not a usable name)" % name)
            continue
        if name.startswith("_"):
            refused.append("%s (names may not begin with an underscore)" % name)
            continue
        if name in taken:
            refused.append("%s (a build123d name already)" % name)
            continue
        try:
            number = float(value)
        except (TypeError, ValueError):
            refused.append("%s (not a number)" % name)
            continue
        # ‼️ An integral value arrives as an INT.
        #
        # Measured live 2026-09-10, and it was a regression this feature caused:
        # the first version handed every parameter over as a float, so
        # `teeth_count` was 20.0 and `range(teeth_count)` raised
        #
        #     TypeError: 'float' object cannot be interpreted as an integer
        #
        # in 6 of 9 runs — worse than before the parameters existed, because the
        # model had been writing `num_teeth = 20` as a literal and it worked.
        # A count IS an integer, and the document has no type to say so; the
        # value itself does.
        #
        # Safe in the other direction: build123d takes an int wherever it wants a
        # float, and Python 3 division is true division, so `5 / 2` is 2.5 whether
        # 5 arrived as an int or a float. Nothing downstream can tell except the
        # places that REQUIRE an int, which is the whole point.
        usable[name] = int(number) if number.is_integer() else number
    return usable, refused


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
    parameters = request.get("parameters") or {}
    cpu = int(request.get("cpu_seconds") or 10)
    mem = int(request.get("memory_bytes") or 1024 * 1024 * 1024)

    try:
        limits(cpu, mem)
    except Exception as exc:  # noqa: BLE001 — a platform without rlimits still runs
        sys.stderr.write("limits unavailable: %s\n" % exc)

    os.chdir(tempfile.mkdtemp(prefix="forge-script-"))

    # Resolved BEFORE the check, because the check has to know these names — a
    # script reading a parameter it declared would otherwise be refused for using
    # it. `taken` is every name the language already owns; a builder wins.
    taken = BUILDER_NAMES | set(ALLOWED_MATH) | set(ALLOWED_BUILTINS) | {"math"}
    params, refused_params = usable_parameters(parameters, taken)
    if refused_params:
        sys.stderr.write("parameters not in scope: %s\n" % ", ".join(refused_params))

    try:
        tree = check(source, params)
    except Refused as exc:
        # The signatures are attached HERE rather than inside check(), because
        # check() runs before the namespace exists — it is the thing that decides
        # whether build123d is worth loading at all. A refusal that suggested a
        # name can now also say how that name is called, and on a machine with no
        # kernel the namespace is empty and it simply says less.
        ns, _ = namespace(True)
        detail = str(exc)
        # A name that is a METHOD is answered as one, and the spelling suggestion
        # is suppressed: `rotate` is real, and pointing at `Rotation` — a
        # Location — sends the repair somewhere useless.
        hint = _method_hint(ns, _refused_name(detail))
        if hint:
            detail = detail.replace(_did_you_mean(_refused_name(detail), _known_names()), "") + hint
        else:
            detail += _suggestion_signatures(ns, detail)
        print(json.dumps({"ok": False, "refused": True, "error": detail}))
        return

    ns = {}
    try:
        uses = {n.id for n in ast.walk(tree)
                if isinstance(n, ast.Name) and isinstance(n.ctx, ast.Load)}
        # Only load OCCT when the script actually names one of its builders:
        # it costs seconds, and the refusals and the limits must be exercisable
        # on a machine with no kernel installed, which is where CI runs.
        # When the kernel is missing, _builder_names() is None and we cannot
        # tell whether a name is a builder — so we try to load it, and the run
        # fails with ModuleNotFoundError, which is the true reason.
        ns, missing = namespace(bool(uses & BUILDER_NAMES))
        if missing:
            sys.stderr.write("not in this build123d: %s\n" % ", ".join(missing))
        # After the namespace is built, and only for names it does not already
        # hold — usable_parameters has already refused the ones that collide, and
        # this is the second half of that rule standing where it cannot be
        # skipped. `__builtins__` is set inside namespace(); a parameter cannot
        # be called that, because a leading underscore is refused above.
        for name, value in params.items():
            ns.setdefault(name, value)
        exec(compile(tree, "<script>", "exec"), ns)  # noqa: S102 — the point of this file
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"ok": False,
                          "error": "%s: %s%s" % (type(exc).__name__, exc,
                                                 signature_help(ns, source, exc)),
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

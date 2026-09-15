"""Split the sidecar's STEP export into its OCCT steps, at one barrel size.

Usage: python profile_export.py SIDECAR REQUEST OUT_JSONL VARIANT[,VARIANT...]
"""
import base64
import gc
import importlib.util
import json
import os
import sys
import tempfile
import time

import psutil

SIDECAR, REQUEST, OUT, VARIANTS = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4].split(",")

spec = importlib.util.spec_from_file_location("sidecar", SIDECAR)
sc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sc)

captured = {}


def no_check(solids, ids, labels, placed=None):
    captured["built"], captured["names"] = solids, labels
    return [], False, 0, {"pairs": 0, "booleans": 0, "reused": 0}


sc._interferences = no_check
with open(REQUEST, "rb") as fh:
    request = json.loads(fh.read())
request["format"] = ""
request["properties"] = False
t = time.perf_counter()
reply = sc._build(request)
build_s = time.perf_counter() - t
del request
built, names = captured["built"], captured["names"]
n = len(built)
CHUNKS = 10


def export(variant):
    from OCP.TCollection import TCollection_ExtendedString
    from OCP.TDocStd import TDocStd_Document
    from OCP.TopLoc import TopLoc_Location
    from OCP.XCAFApp import XCAFApp_Application
    from OCP.XCAFDoc import XCAFDoc_DocumentTool

    st = {}
    chunk_s = [0.0] * CHUNKS
    t0 = time.perf_counter()
    doc = TDocStd_Document(TCollection_ExtendedString("XmlOcaf"))
    app = XCAFApp_Application.GetApplication_s()
    app.NewDocument(TCollection_ExtendedString("MDTV-XCAF"), doc)
    app.InitDocument(doc)
    XCAFDoc_DocumentTool.SetLengthUnit_s(doc, 0.001)
    tool = XCAFDoc_DocumentTool.ShapeTool_s(doc.Main())
    root = tool.NewShape()
    sc._label_name(root, "COMPOUND")
    st["doc_s"] = time.perf_counter() - t0

    add_shape = name_def = add_comp = name_comp = 0.0
    identity = TopLoc_Location()
    pc = time.perf_counter
    step = max(n // CHUNKS, 1)
    cstart = pc()
    parent = root
    for i, (solid, name) in enumerate(zip(built, names)):
        if variant == "chunked" and i % 4096 == 0:
            parent = tool.NewShape()
            sc._label_name(parent, "GROUP %d" % (i // 4096))
            tool.AddComponent(root, parent, identity)
        a = pc()
        label = tool.AddShape(solid.wrapped.Located(identity), False, False)
        b = pc()
        if variant != "no_def_names":
            sc._label_name(label, name)
        c = pc()
        comp = tool.AddComponent(parent, label, solid.wrapped.Location())
        d = pc()
        if variant != "no_comp_names":
            sc._label_name(comp, name)
        e = pc()
        add_shape += b - a
        name_def += c - b
        add_comp += d - c
        name_comp += e - d
        if (i + 1) % step == 0 and (i + 1) // step <= CHUNKS:
            chunk_s[(i + 1) // step - 1] = pc() - cstart
            cstart = pc()
    st.update(add_shape_s=add_shape, name_def_s=name_def, add_comp_s=add_comp, name_comp_s=name_comp,
              loop_chunks_s=chunk_s)
    t = pc()
    tool.UpdateAssemblies()
    st["update_assemblies_s"] = pc() - t

    from OCP.APIHeaderSection import APIHeaderSection_MakeHeader
    from OCP.IFSelect import IFSelect_ReturnStatus
    from OCP.Interface import Interface_Static
    from OCP.Message import Message, Message_Gravity
    from OCP.STEPCAFControl import STEPCAFControl_Controller, STEPCAFControl_Writer
    from OCP.STEPControl import STEPControl_Controller, STEPControl_StepModelType
    from OCP.TCollection import TCollection_HAsciiString
    from OCP.XSControl import XSControl_WorkSession
    from build123d import PrecisionMode

    t = pc()
    for printer in Message.DefaultMessenger_s().Printers():
        printer.SetTraceLevel(Message_Gravity.Message_Fail)
    writer = STEPCAFControl_Writer(XSControl_WorkSession(), False)
    writer.SetColorMode(variant not in ("no_color_layer", "bare"))
    writer.SetLayerMode(variant not in ("no_color_layer", "bare"))
    writer.SetNameMode(variant not in ("no_name_mode", "bare"))
    # The writer's other modes, default on; each can be switched off alone.
    for mode, setter in (("shuo", "SetSHUOMode"), ("props", "SetPropsMode"), ("gdt", "SetDimTolMode"),
                         ("material", "SetMaterialMode"), ("vismat", "SetVisualMaterialMode"),
                         ("metadata", "SetMetadataMode")):
        if variant in ("no_" + mode, "all_off") and hasattr(writer, setter):
            getattr(writer, setter)(False)
    if variant == "all_off":
        writer.SetColorMode(False)
        writer.SetLayerMode(False)
        writer.SetNameMode(False)
    # Props off (the fix) plus one more mode off, to find what is left superlinear.
    if variant.startswith("fix"):
        writer.SetPropsMode(False)
    if variant == "fix_no_names":
        writer.SetNameMode(False)
    if variant == "fix_no_color_layer":
        writer.SetColorMode(False)
        writer.SetLayerMode(False)
    if variant == "fix_no_shuo":
        writer.SetSHUOMode(False)
    if variant == "fix_no_gdt_material":
        writer.SetDimTolMode(False)
        writer.SetMaterialMode(False)
    st["modes"] = {m: getattr(writer, g)() for m, g in (
        ("name", "GetNameMode"), ("color", "GetColorMode"), ("layer", "GetLayerMode"),
        ("shuo", "GetSHUOMode"), ("props", "GetPropsMode"), ("gdt", "GetDimTolMode"),
        ("material", "GetMaterialMode"), ("vismat", "GetVisualMaterialMode"),
        ("metadata", "GetMetadataMode")) if hasattr(writer, g)}
    header = APIHeaderSection_MakeHeader(writer.Writer().Model())
    if not header.IsDone():
        header = APIHeaderSection_MakeHeader(0)
        header.Apply(writer.Writer().Model())
    header.SetOriginatingSystem(TCollection_HAsciiString("build123d"))
    STEPCAFControl_Controller.Init_s()
    STEPControl_Controller.Init_s()
    Interface_Static.SetIVal_s("write.surfacecurve.mode", 1)
    Interface_Static.SetIVal_s("write.precision.mode", PrecisionMode.AVERAGE.value)
    st["writer_setup_s"] = pc() - t
    if variant == "plain_writer":
        # The base actor alone: STEPControl_Writer on the compound UpdateAssemblies
        # rebuilt, in assembly mode, with no XCAF layer (no names).
        from OCP.STEPControl import STEPControl_Writer
        from OCP.XCAFDoc import XCAFDoc_ShapeTool
        writer = STEPControl_Writer()
        Interface_Static.SetIVal_s("write.step.assembly", 1)
        compound = XCAFDoc_ShapeTool.GetShape_s(root)
    t = pc()
    if variant == "plain_writer":
        writer.Transfer(compound, STEPControl_StepModelType.STEPControl_AsIs)
    else:
        writer.Transfer(doc, STEPControl_StepModelType.STEPControl_AsIs)
    st["transfer_s"] = pc() - t
    fd, path = tempfile.mkstemp(suffix=".step")
    os.close(fd)
    t = pc()
    ok = writer.Write(path) == IFSelect_ReturnStatus.IFSelect_RetDone
    st["write_s"] = pc() - t
    t = pc()
    with open(path, "rb") as fh:
        body = fh.read()
    b64 = base64.b64encode(body).decode("ascii")
    st["read_b64_s"] = pc() - t
    st["bytes"] = len(body)
    st["nauo"] = body.count(b"NEXT_ASSEMBLY_USAGE_OCCURRENCE(")
    st["breps"] = body.count(b"MANIFOLD_SOLID_BREP(")
    st["write_ok"] = ok
    st["total_s"] = pc() - t0
    keep = os.environ.get("PROFILE_KEEP")
    if keep:
        os.replace(path, keep)
    else:
        os.unlink(path)
    del b64, body, writer
    try:
        app.Close(doc)
    except Exception as exc:
        st["close_error"] = str(exc)
    del doc, tool, root
    gc.collect()
    return st


for variant in VARIANTS:
    psutil.cpu_percent(None)
    wall = time.time()
    st = export(variant)
    row = {"n": n, "variant": variant, "build_s": build_s, "wall_s": time.time() - wall,
           "system_cpu_pct": psutil.cpu_percent(None),
           "peak_rss_gb": getattr(psutil.Process().memory_info(), "peak_wset", 0) / 1e9}
    row.update(st)
    print(json.dumps(row))
    sys.stdout.flush()
    with open(OUT, "a") as fh:
        fh.write(json.dumps(row) + "\n")

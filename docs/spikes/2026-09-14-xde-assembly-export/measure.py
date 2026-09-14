"""Spike (Phase 4, stage K2): export N occurrences of one definition as STEP, two ways.

  compound -- what the sidecar does before K2: Compound(children=located copies), each
              child labelled, written by build123d's export_step.
  xde      -- an XDE assembly: the definition is added ONCE as a shape label, and every
              occurrence is a component that refers to it with its own TopLoc_Location
              and its own name, written by STEPCAFControl_Writer.

For each N it reports the time to assemble, the time to write, the file size, and what
the file SAYS, counted two independent ways:
  - in the STEP text: MANIFOLD_SOLID_BREP (solid bodies written), PRODUCT( (product
    definitions), NEXT_ASSEMBLY_USAGE_OCCURRENCE (instances);
  - by re-importing it with STEPCAFControl_Reader: components under the top assembly,
    distinct shapes they refer to, and the total volume of the located components.

Run:  .cadvenv/bin/python docs/spikes/2026-09-14-xde-assembly-export/measure.py [N ...]
"""
import os
import re
import sys
import tempfile
import time

from build123d import Box, Compound, Location, Plane, Vector, export_step
from OCP.BRepGProp import BRepGProp
from OCP.GProp import GProp_GProps
from OCP.IFSelect import IFSelect_ReturnStatus
from OCP.STEPCAFControl import STEPCAFControl_Reader, STEPCAFControl_Writer
from OCP.STEPControl import STEPControl_StepModelType
from OCP.TCollection import TCollection_AsciiString, TCollection_ExtendedString
from OCP.TDataStd import TDataStd_Name
from OCP.TDF import TDF_Label, TDF_LabelSequence, TDF_Tool
from OCP.TDocStd import TDocStd_Document
from OCP.XCAFApp import XCAFApp_Application
from OCP.XCAFDoc import XCAFDoc_DocumentTool, XCAFDoc_ShapeTool
from OCP.XSControl import XSControl_WorkSession

SIZE = 10.0
PITCH = 30.0  # far enough apart that no two boxes meet


def occurrences(n):
    side = int(n ** 0.5) + 1
    return [Location(Plane(origin=Vector(PITCH * (i % side), PITCH * (i // side), 0)))
            for i in range(n)]


def new_doc():
    doc = TDocStd_Document(TCollection_ExtendedString("XmlOcaf"))
    app = XCAFApp_Application.GetApplication_s()
    app.NewDocument(TCollection_ExtendedString("MDTV-XCAF"), doc)
    app.InitDocument(doc)
    XCAFDoc_DocumentTool.SetLengthUnit_s(doc, 0.001)
    return doc


def name(label, text):
    TDataStd_Name.Set_s(label, TCollection_ExtendedString(text))


def write_compound(n, path):
    base = Box(SIZE, SIZE, SIZE)
    t0 = time.perf_counter()
    copies = [loc * base for loc in occurrences(n)]
    assembly = Compound(children=copies)
    for i, child in enumerate(assembly.children):
        child.label = "box-%d" % (i + 1)
    t1 = time.perf_counter()
    export_step(assembly, path)
    return t1 - t0, time.perf_counter() - t1


def write_xde(n, path):
    base = Box(SIZE, SIZE, SIZE)
    t0 = time.perf_counter()
    doc = new_doc()
    tool = XCAFDoc_DocumentTool.ShapeTool_s(doc.Main())
    definition = tool.AddShape(base.wrapped, False, False)
    name(definition, "box")
    root = tool.NewShape()
    name(root, "assembly")
    for i, loc in enumerate(occurrences(n)):
        component = tool.AddComponent(root, definition, loc.wrapped)
        name(component, "box-%d" % (i + 1))
    tool.UpdateAssemblies()
    t1 = time.perf_counter()
    writer = STEPCAFControl_Writer(XSControl_WorkSession(), False)
    writer.SetNameMode(True)
    writer.Transfer(doc, STEPControl_StepModelType.STEPControl_AsIs)
    if writer.Write(path) != IFSelect_ReturnStatus.IFSelect_RetDone:
        raise RuntimeError("STEPCAFControl_Writer failed")
    return t1 - t0, time.perf_counter() - t1


def text_counts(path):
    body = open(path, "r", errors="replace").read()
    return (len(re.findall(r"=\s*MANIFOLD_SOLID_BREP\(", body)),
            len(re.findall(r"=\s*PRODUCT\(", body)),
            len(re.findall(r"=\s*NEXT_ASSEMBLY_USAGE_OCCURRENCE\(", body)))


def reimport(path):
    """Components under the top assembly, distinct referred shapes, total volume, and how many
    components came back carrying their own occurrence name (box-1, box-2, ...)."""
    t0 = time.perf_counter()
    doc = new_doc()
    reader = STEPCAFControl_Reader()
    reader.SetNameMode(True)
    if reader.ReadFile(path) != IFSelect_ReturnStatus.IFSelect_RetDone or not reader.Transfer(doc):
        raise RuntimeError("could not read back " + path)
    tool = XCAFDoc_DocumentTool.ShapeTool_s(doc.Main())
    free = TDF_LabelSequence()
    tool.GetFreeShapes(free)
    components, referred, volume, named = 0, set(), 0.0, set()
    for f in range(1, free.Length() + 1):
        top = free.Value(f)
        if not XCAFDoc_ShapeTool.IsAssembly_s(top):
            continue
        kids = TDF_LabelSequence()
        XCAFDoc_ShapeTool.GetComponents_s(top, kids, True)
        for k in range(1, kids.Length() + 1):
            label = kids.Value(k)
            target = TDF_Label()
            if not XCAFDoc_ShapeTool.GetReferredShape_s(label, target):
                continue
            if XCAFDoc_ShapeTool.IsAssembly_s(target):
                continue  # a sub-assembly; its own components are counted by getsubchilds
            components += 1
            attr = TDataStd_Name()
            if label.FindAttribute(TDataStd_Name.GetID_s(), attr):
                named.add(TCollection_AsciiString(attr.Get()).ToCString())
            entry = TCollection_AsciiString()
            TDF_Tool.Entry_s(target, entry)
            referred.add(entry.ToCString())
            props = GProp_GProps()
            BRepGProp.VolumeProperties_s(XCAFDoc_ShapeTool.GetShape_s(label), props)
            volume += props.Mass()
    return components, len(referred), volume, named, time.perf_counter() - t0


def main():
    sizes = [int(a) for a in sys.argv[1:]] or [1000, 4096, 10000]
    exact = SIZE ** 3
    print("%-8s %6s %10s %8s %9s %6s %8s %9s %10s %8s %10s %s" % (
        "mode", "N", "assemble s", "write s", "bytes", "breps", "products", "instances",
        "components", "shapes", "names kept", "volume exact"))
    for n in sizes:
        for mode, write in (("compound", write_compound), ("xde", write_xde)):
            fd, path = tempfile.mkstemp(suffix=".step")
            os.close(fd)
            try:
                assemble, written = write(n, path)
                breps, products, instances = text_counts(path)
                components, shapes, volume, named, _ = reimport(path)
                kept = len(named & {"box-%d" % (i + 1) for i in range(n)})
                print("%-8s %6d %10.2f %8.2f %9d %6d %8d %9d %10d %8d %10d %s" % (
                    mode, n, assemble, written, os.path.getsize(path), breps, products, instances,
                    components, shapes, kept, abs(volume - n * exact) <= 1e-6 * n * exact))
                sys.stdout.flush()
            finally:
                os.unlink(path)


if __name__ == "__main__":
    main()

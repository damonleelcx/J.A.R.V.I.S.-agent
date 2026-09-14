"""Read a STEP file back the way a CAD tool would, and say what is in it.

Used by the kernel tests (Phase 4, stage K2): the sidecar's own numbers (volume,
bounds) are measured on the solids it BUILT, so they stay correct even when the
file it WRITES puts a part in the wrong place. Only reading the file back can
catch that. Measured 2026-09-14: a component written without its location, or a
shared shape written with its location left on, both passed every check on the
build's own numbers.

Usage: python step_reimport.py FILE.step
Prints one JSON object:
  components  the solid instances under the top assembly
  shapes      how many distinct shapes they refer to
  names       the instances' names, in file order
  bounds      the extent of everything in the file: minX, minY, minZ, maxX, maxY, maxZ
"""
import json
import sys

from OCP.Bnd import Bnd_Box
from OCP.BRepBndLib import BRepBndLib
from OCP.IFSelect import IFSelect_ReturnStatus
from OCP.STEPCAFControl import STEPCAFControl_Reader
from OCP.TCollection import TCollection_AsciiString, TCollection_ExtendedString
from OCP.TDataStd import TDataStd_Name
from OCP.TDF import TDF_Label, TDF_LabelSequence, TDF_Tool
from OCP.TDocStd import TDocStd_Document
from OCP.XCAFApp import XCAFApp_Application
from OCP.XCAFDoc import XCAFDoc_DocumentTool, XCAFDoc_ShapeTool


def main(path):
    doc = TDocStd_Document(TCollection_ExtendedString("XmlOcaf"))
    application = XCAFApp_Application.GetApplication_s()
    application.NewDocument(TCollection_ExtendedString("MDTV-XCAF"), doc)
    application.InitDocument(doc)
    reader = STEPCAFControl_Reader()
    reader.SetNameMode(True)
    if reader.ReadFile(path) != IFSelect_ReturnStatus.IFSelect_RetDone or not reader.Transfer(doc):
        sys.exit("could not read " + path)

    tool = XCAFDoc_DocumentTool.ShapeTool_s(doc.Main())
    free = TDF_LabelSequence()
    tool.GetFreeShapes(free)
    box = Bnd_Box()
    components, shapes, names = 0, set(), []
    for f in range(1, free.Length() + 1):
        top = free.Value(f)
        BRepBndLib.Add_s(XCAFDoc_ShapeTool.GetShape_s(top), box)
        if not XCAFDoc_ShapeTool.IsAssembly_s(top):
            continue
        kids = TDF_LabelSequence()
        XCAFDoc_ShapeTool.GetComponents_s(top, kids, True)
        for k in range(1, kids.Length() + 1):
            label = kids.Value(k)
            target = TDF_Label()
            if not XCAFDoc_ShapeTool.GetReferredShape_s(label, target) or XCAFDoc_ShapeTool.IsAssembly_s(target):
                continue
            components += 1
            entry = TCollection_AsciiString()
            TDF_Tool.Entry_s(target, entry)
            shapes.add(entry.ToCString())
            attr = TDataStd_Name()
            names.append(TCollection_AsciiString(attr.Get()).ToCString()
                         if label.FindAttribute(TDataStd_Name.GetID_s(), attr) else "")

    print(json.dumps({"components": components, "shapes": len(shapes), "names": names,
                      "bounds": list(box.Get())}))


if __name__ == "__main__":
    main(sys.argv[1])

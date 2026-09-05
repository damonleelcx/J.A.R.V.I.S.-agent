package geometry

import (
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Export (PRD VIS-05).
//
// "Preview meshes and, where adapters permit, editable parametric export; label
// tessellation, inference, lossy conversion."
//
// # What this build can and cannot do, and why the refusal is the feature
//
// There is no CAD kernel IN THIS PACKAGE, and that is still deliberate: the
// kernel is a subsystem that can be absent and this is the vocabulary
// (internal/domain/cad, wave 14). What follows describes why the parametric
// formats were refused outright before one existed, and why the refusal is now a
// question of deployment rather than of build.
//
// STEP and KCL are parametric formats — a STEP file
// is a B-Rep with analytic surfaces, and producing one from a bag of primitives
// is the kernel's job, not a serialiser's. The Zoo spike measured what real
// parametric output looks like and what it costs to obtain
// (docs/spikes/2026-09-02-zoo-text-to-cad/): a websocket a browser cannot open,
// a client that carries project state, and a 56 MB CLI to export.
//
// So parametric export is DECLARED and REFUSED, with the reason, exactly as the
// unavailable connectors are (internal/tools/unavailable.go). The alternative —
// leaving it out — is what invites somebody to write a STEP file full of
// tessellated facets and call it parametric, which is a lie with a file
// extension on it.
//
// # Why the mesh formats refuse an assembly with no unit
//
// A mesh file's numbers become a length in whatever opens it, and no mesh format
// carries a scale that its consumers act on: a slicer reading 60 will print
// 60 mm. On screen an unstated unit is survivable, because "60 (unit not
// stated)" is printed beside the number — the label travels with the geometry.
// In a downloaded file it cannot travel to the thing that matters, which is the
// machine at the other end. So export refuses, and says the one sentence that
// fixes it.

// FormatKind is what sort of thing a format holds.
type FormatKind string

const (
	// KindMesh is triangles. Everything this build can write.
	KindMesh FormatKind = "mesh"
	// KindParametric is a model with features, constraints and dimensions that
	// can be edited. Nothing in this build can produce one.
	KindParametric FormatKind = "parametric"
)

// Format is one export target.
//
// A table rather than a switch: what a deployment can and cannot emit is a
// question people ask directly, so it is answerable by reading one list and by
// GET /v1/geometry/formats, rather than by tracing branches.
type Format struct {
	Name      string
	Extension string
	MediaType string
	Kind      FormatKind
	Available bool
	Gloss     string
	// Reason is why an unavailable format is unavailable, and what a person can
	// do instead. Empty when Available.
	Reason string
}

var formats = []Format{
	{
		Name: "obj", Extension: ".obj", MediaType: "model/obj", Kind: KindMesh, Available: true,
		Gloss: "Wavefront OBJ. Triangles with one group per part, and the conversion label written " +
			"into the file as comments.",
	},
	{
		Name: "stl", Extension: ".stl", MediaType: "model/stl", Kind: KindMesh, Available: true,
		Gloss: "ASCII STL. Triangles only — the format has no place for part identity, colour, or " +
			"any of the label, so all of it is lost at the file boundary.",
	},
	{
		Name: "step", Extension: ".step", MediaType: "model/step", Kind: KindParametric,
		Gloss: "ISO 10303 STEP — a B-Rep solid with analytic surfaces.",
		// The only format whose availability is decided at RUN TIME. Everything
		// else here is a property of the build; this is a property of the
		// deployment, and Formats is told which it is (wave 14).
		Reason: "This deployment has no CAD kernel configured, so there is no B-Rep to write. " +
			"Set FORGE_CAD_PYTHON to a Python interpreter with build123d installed. " +
			"Until then, a STEP file containing tessellated facets would be a mesh with a " +
			"parametric extension, which is worse than no file: everything downstream would " +
			"treat it as an exact solid.",
	},
	{
		Name: "kcl", Extension: ".kcl", MediaType: "text/plain", Kind: KindParametric,
		Gloss: "KCL — Zoo's parametric source: named parameters, constrained sketches, extrusions.",
		Reason: "This build proposes geometry as loose primitives with no parameters and no constraints, " +
			"so there is nothing parametric to write down. Emitting KCL whose every dimension is a " +
			"hard-coded literal would produce a file that looks editable and is not.",
	},
	{
		Name: "iges", Extension: ".igs", MediaType: "model/iges", Kind: KindParametric,
		Gloss: "IGES — surfaces and curves for exchange with older CAD systems.",
		// Refused even where a kernel IS configured: nothing in this build
		// writes IGES, and marking it available because a neighbouring format
		// became so is how a capability list starts lying.
		Reason: "Nothing in this build writes IGES, so it is refused whether or not a CAD kernel " +
			"is configured. Export STEP where a kernel is available, or the mesh otherwise, " +
			"and rebuild the part in a CAD tool.",
	},
}

// Formats returns every declared export target, available or not.
//
// Including the unavailable ones is the point: a person who cannot find STEP in
// a list concludes it was forgotten, and a model that cannot find it invents
// something. Both are answered by the format being present and saying why not.
func Formats(hasKernel bool) []Format {
	out := append([]Format(nil), formats...)
	if !hasKernel {
		return out
	}
	// STEP, and ONLY step. IGES is still refused with a kernel present: nothing
	// in this build writes it, and marking it available because a neighbouring
	// format became so is how a list starts lying. KCL is refused for a reason
	// the kernel does not touch either — see its gloss.
	for i := range out {
		if out[i].Name == "step" {
			out[i].Available = true
			out[i].Reason = ""
		}
	}
	return out
}

// FormatOf resolves a format name.
func FormatOf(name string) (Format, error) {
	const op = "geometry.FormatOf"

	want := strings.ToLower(strings.TrimSpace(name))
	for _, f := range formats {
		if f.Name == want {
			return f, nil
		}
	}
	names := make([]string, 0, len(formats))
	for _, f := range formats {
		names = append(names, f.Name)
	}
	return Format{}, errs.New(op, errs.CodeValidationFailed).
		WithDetail("%q is not an export format FORGE knows. Declared formats are: %s.",
			name, strings.Join(names, ", "))
}

// Label is everything an export does NOT preserve (PRD VIS-05).
//
// Computed from the variant and the format, never stored: it describes a
// conversion, and a conversion that has not happened yet has no history to keep.
type Label struct {
	Format       string
	FormatKind   FormatKind
	Units        Unit
	Frame        Frame
	Generator    string
	Verification string
	Disposition  string
	// Tessellation is one entry per curved part, with the error measured.
	Tessellation []Deviation
	// Inference is what the exporter decided because the document did not say.
	Inference []string
	// Lossy is what this format cannot carry out of the door.
	Lossy []string
	// Assumptions and NotVerified are carried out of the document unchanged.
	// They are the reason the file exists at all and the reason it must not be
	// trusted, and they are the first thing lost when geometry is passed on as
	// a file, so they are written into the ones that have room for them.
	Assumptions []string
	NotVerified []string
}

// Headline is the one sentence that must survive being skim-read.
func (l *Label) Headline() string {
	return "This is a tessellated preview of an unverified proposal. " +
		"It establishes nothing about manufacturability, strength, fit, or compliance."
}

// LabelFor computes what exporting this variant to this format would lose.
//
// Separate from Export so a person can be told BEFORE they download, and so the
// workbench can show it beside the button rather than after the click.
func LabelFor(v *Variant, format string) (*Label, *Mesh, error) {
	const op = "geometry.LabelFor"

	f, err := FormatOf(format)
	if err != nil {
		return nil, nil, err
	}
	if !f.Available {
		return nil, nil, errs.New(op, errs.CodeConnectorUnavailable).
			WithDetail("%s export is declared but has no backend in this deployment. %s", f.Name, f.Reason).
			WithField("format", f.Name)
	}
	if !v.Units.Known() {
		return nil, nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("this variant has no unit FORGE can convert (%s), and a mesh file's numbers "+
				"become a length in whatever opens it — a slicer reading 60 will print 60 mm. "+
				"Ask FORGE to restate the assembly in mm, cm, m or in, then export that variant.",
				strings.ToLower(strings.TrimSuffix(v.UnitsNote(), ".")))
	}

	mesh := Tessellate(v.Document, v.Units)
	label := &Label{
		Format: f.Name, FormatKind: f.Kind,
		Units: v.Units, Frame: v.Frame, Generator: v.Generator,
		Verification: string(v.Verification), Disposition: string(v.Disposition),
		Tessellation: mesh.Deviations,
		Inference:    mesh.Inferences,
		Lossy:        lossyFor(f, v),
		Assumptions:  v.Assumptions(),
		NotVerified:  v.NotVerified(),
	}
	return label, mesh, nil
}

// lossyFor is what a given format drops on the way out.
//
// Written per format rather than as one generic paragraph, because "colour is
// lost" and "which triangles belong to which part is lost" are different sizes
// of problem and only the second one changes what somebody can do with the file.
func lossyFor(f Format, v *Variant) []string {
	out := []string{
		fmt.Sprintf("Dimensions are in %s. No mesh format records its own unit, so whatever opens "+
			"this file will apply its own default — check it before printing or machining.", v.Units),
		"Curved surfaces are gone. What is in the file is flat faces, listed above with the error each one carries.",
	}
	switch f.Name {
	case "obj":
		out = append(out,
			"Colour and transparency are not written. They were display choices, not part properties.",
			"The per-part notes, the assumptions and the unverified list are COMMENTS. Most tools "+
				"discard them on import, so the file arrives with no provenance attached.")
	case "stl":
		out = append(out,
			"Part identity is gone. STL is one unnamed soup of triangles: the parts list, the part "+
				"names, and which facet belongs to which part do not survive.",
			"Nothing of this label is in the file. STL has no comments — only the solid's name line "+
				"carries anything, and most readers ignore it.",
			"Colour and transparency are not written.")
	}
	for _, p := range v.Document.Parts {
		if p.Shape == "plane" {
			out = append(out, fmt.Sprintf("%q is a plane: two triangles with no thickness. "+
				"It is not a solid and nothing can be made from it.", p.Label()))
		}
	}
	return out
}

// Result is a rendered export.
type Result struct {
	Format   Format
	Filename string
	Content  []byte
	Label    *Label
	// Triangles is how many facets were written — the honest measure of what a
	// person is downloading, and the number that goes in the log.
	Triangles int
}

// Export renders a variant into a mesh file.
func Export(v *Variant, format string) (*Result, error) {
	label, mesh, err := LabelFor(v, format)
	if err != nil {
		return nil, err
	}
	f, _ := FormatOf(format)

	var content []byte
	switch f.Name {
	case "obj":
		content = writeOBJ(v, mesh, label)
	case "stl":
		content = writeSTL(v, mesh)
	default:
		// Unreachable while every available format has a writer, and a loud
		// failure rather than an empty file if one is ever added without one.
		return nil, errs.New("geometry.Export", errs.CodeInternal).
			WithDetail("format %q is declared available but this build has no writer for it", f.Name)
	}
	return &Result{
		Format: f, Filename: Filename(v, f), Content: content,
		Label: label, Triangles: len(mesh.Triangles()),
	}, nil
}

// Filename names the download after the variant and its version, so two
// downloads of the same assembly do not overwrite each other in a downloads
// folder and become indistinguishable.
//
// Exported because the parametric export path lives outside this package (the
// kernel is a subsystem, the vocabulary is not) and a second naming rule would
// mean two files of the same variant landing under different names.
func Filename(v *Variant, f Format) string {
	base := strings.TrimSuffix(strings.TrimPrefix(v.Path, "geometry/"), ".forge.json")
	if base == "" {
		base = "geometry"
	}
	return fmt.Sprintf("%s-v%d%s", base, v.Version, f.Extension)
}

// ---------------------------------------------------------------------------
// Writers
// ---------------------------------------------------------------------------

// writeOBJ renders Wavefront OBJ with the label as a comment header.
//
// OBJ is the format that keeps the most of what matters here: one group per
// part, so the parts list survives the trip, and comments, so the provenance
// travels as far as a text editor. Not as far as a slicer — that is in the
// lossy list, said plainly rather than implied by the label existing.
func writeOBJ(v *Variant, mesh *Mesh, label *Label) []byte {
	var b strings.Builder
	c := func(format string, args ...any) {
		b.WriteString("# " + fmt.Sprintf(format, args...) + "\n")
	}

	c("%s", label.Headline())
	c("")
	c("Produced by FORGE. %s", v.Name)
	c("Variant:      %s v%d (%s)", v.Path, v.Version, v.VersionID)
	c("Generator:    %s", v.Generator)
	c("Units:        %s — OBJ itself records no unit; this comment is the only statement of scale.", v.Units)
	c("Frame:        %s", v.Frame)
	c("Verification: %s (what a machine found)", label.Verification)
	c("Disposition:  %s (what a person decided)", label.Disposition)
	c("Facets:       %d", len(mesh.Triangles()))
	c("")

	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		c("%s", title)
		for _, l := range lines {
			c("  - %s", l)
		}
		c("")
	}
	if len(label.Tessellation) > 0 {
		c("TESSELLATION — every curve below is flat faces in this file:")
		for _, d := range label.Tessellation {
			c("  - %s (%s): %d faces; the exported surface lies up to %s inside the one described.",
				d.Label, d.Shape, d.Segments, d.Max)
		}
		c("")
	}
	section("INFERRED — decided by FORGE because the description did not say:", label.Inference)
	section("LOST IN THIS CONVERSION:", label.Lossy)
	section("ASSUMED, NOT SPECIFIED:", label.Assumptions)
	section("THIS FILE DOES NOT ESTABLISH:", label.NotVerified)

	b.WriteString("o " + objName(v.Name) + "\n")

	// One shared vertex list, groups indexing into it. Vertices are NOT
	// deduplicated: two facets meeting at an edge write the corner twice. It
	// costs size and buys correctness — merging them requires a tolerance, and a
	// tolerance chosen here would silently weld together parts that were
	// touching on purpose.
	var index int
	for _, g := range mesh.Groups {
		b.WriteString("g " + objName(g.Label) + "\n")
		var faces strings.Builder
		for _, t := range g.Triangles {
			for _, p := range [][3]float64{t.A, t.B, t.C} {
				fmt.Fprintf(&b, "v %s %s %s\n", num(p[0]), num(p[1]), num(p[2]))
			}
			fmt.Fprintf(&b, "vn %s %s %s\n", num(t.Normal[0]), num(t.Normal[1]), num(t.Normal[2]))
			a, bb, cc := index+1, index+2, index+3
			n := index/3 + 1
			fmt.Fprintf(&faces, "f %d//%d %d//%d %d//%d\n", a, n, bb, n, cc, n)
			index += 3
		}
		b.WriteString(faces.String())
	}
	return []byte(b.String())
}

// objName makes a token OBJ's parser will read as one name.
func objName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "part"
	}
	return strings.Join(strings.Fields(s), "_")
}

// writeSTL renders ASCII STL.
//
// ASCII rather than binary for one reason that outweighs the size: the solid's
// name line is plain text, so the one sentence that matters is at least IN the
// file rather than in an 80-byte header field that no text editor shows. It is
// still recorded as lost, because a reader that shows the user a solid name is
// rare and this must not be mistaken for the label travelling.
func writeSTL(v *Variant, mesh *Mesh) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "solid FORGE_%s_v%d_units_%s_UNVERIFIED_PROPOSAL\n",
		objName(v.Name), v.Version, v.Units)
	for _, t := range mesh.Triangles() {
		fmt.Fprintf(&b, "  facet normal %s %s %s\n", num(t.Normal[0]), num(t.Normal[1]), num(t.Normal[2]))
		b.WriteString("    outer loop\n")
		for _, p := range [][3]float64{t.A, t.B, t.C} {
			fmt.Fprintf(&b, "      vertex %s %s %s\n", num(p[0]), num(p[1]), num(p[2]))
		}
		b.WriteString("    endloop\n")
		b.WriteString("  endfacet\n")
	}
	fmt.Fprintf(&b, "endsolid FORGE_%s_v%d\n", objName(v.Name), v.Version)
	return []byte(b.String())
}

// num formats a coordinate.
//
// Six decimal places, fixed: a tessellated vertex is an irrational number in
// almost every case, and %g would write some of them in exponent form. Several
// widely-used STL and OBJ readers do not accept exponents, and the failure is a
// file that opens empty rather than an error anybody can act on.
func num(v float64) string {
	s := fmt.Sprintf("%.6f", v)
	// -0.000000 is the same point as 0.000000 and reads as a mistake.
	if s == "-0.000000" {
		return "0.000000"
	}
	return s
}

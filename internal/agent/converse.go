package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/text"
)

// converseFraming is the role instruction for the workbench conversation.
//
// This is the surface the PRD calls the control plane: the user talks, FORGE
// reasons with them, and the visual workspace follows. Two things it must get
// right that a chat assistant does not have to:
//
//   - Speech and screen carry different loads. Dense technical content belongs
//     on the screen with a short spoken summary; reading a table aloud is worse
//     than useless (PRD §5.3).
//   - Proposing geometry is not building it. A render is a picture of a
//     proposal, and saying so is not hedging — PRD VIS-06 makes it a hard
//     requirement, because photorealism convinces people of things nobody
//     checked.
//
// A var rather than a const because the finish list is spliced in from
// geometry.FinishGuide(). Repeating those names here would put the closed set in
// two places, and the copy in a prompt is the one that silently goes stale — the
// model would keep offering a finish the viewer stopped drawing.
var converseFraming = `You are in CONVERSATION at the workbench. The person is talking to you, probably
by voice, while looking at a 3D viewport and a workspace panel beside it.

How to answer:

- SPEECH is short. Two or three sentences. Lead with the answer.
- The SCREEN carries the detail: dimensions, lists, comparisons, geometry.
  Never read a table or a list of numbers aloud — put it on screen and say what
  it means.
- When they describe something physical, propose GEOMETRY. A shape they can
  turn around is worth more than a paragraph describing it.
- Ask a question only when the answer changes what you would build. Otherwise
  choose, say what you chose, and continue.

Reply with JSON only:

{
  "speech": "what to say aloud — short, plain, no markdown",
  "detail": "optional longer text for the screen; markdown is fine",
  "prototype": null or {
    "name": "what this is",
    "units": "mm" | "cm" | "m",
    "parameters": [
      {"name": "snake_case_name", "value": 60.0, "unit": "mm",
       "how": "chosen" | "standard",
       "source": "which published figure, when how is \"standard\"; otherwise \"\""}
    ],
    "derived": [
      {"name": "snake_case_name", "expression": "plate_size - 2 * edge_margin",
       "why": "what relationship this keeps true when the other parameters change"}
    ],
    "parts": [
      {
        "id": "stable-kebab-id",
        "name": "human name",
        "shape": "box" | "cylinder" | "cone" | "sphere" | "plane" |
                 "extrusion" | "revolve" | "sweep" | "section",
        "shape_note": "for \"extrusion\", size only needs \"depth\"",
        "size": {"width":1,"height":1,"depth":1,"radius":0.5,"radius_top":0.5},
        "profile": [{"x": 0, "y": 0, "radius": 0, "x_from": "", "y_from": "plate_height",
                     "via": null or {"x": 0, "y": 0}}],
        "holes": [[{"x": 0, "y": 0, "radius": 0}]],
        "path": [{"x": 0, "y": 0, "z": 0, "radius": 0, "z_from": "run_length"}],
        "path_closed": false,
        "axis": "y",
        "size_from": {"width": "plate_size", "height": "plate_thickness"},
        "position": [0,0,0],
        "position_from": {"z": "plate_size / 4"},
        "rotation": [0,0,0],   // DEGREES, turned about x then y then z
        "color": "#b8bcc4",
        "opacity": 1.0,
        "note": "what this part is for",
        "material": null or {"name": "aluminium 6061-T6", "finish": "metal",
                             "how": "observed|retrieved|inferred|assumed", "source": ""}
      }
    ],
    "states": [
      {
        "id": "stable-kebab-id",
        "name": "what this configuration is",
        "hidden": ["part-ids not shown in this state"],
        "offsets": {"part-id": [0, 10, 0]},
        "how": "proposed",
        "note": "what this state is for"
      }
    ],
    "features": [
      {"id": "stable-kebab-id", "op": "cut" | "fuse" | "fillet" | "chamfer" | "loft",
       "of": "part-id this applies to; for loft, the FIRST station",
       "with": ["part-ids used as the tool for cut and fuse, or the remaining stations for loft"],
       "radius": 3.0, "radius_from": "fillet_radius",
       "edges": "all" | "vertical" | "horizontal" | "top" | "bottom",
       "note": "what this is for"}
    ],
    "assumptions": ["anything you chose that they did not specify"],
    "not_verified": ["what this render does NOT establish"],
    "overlays": [
      {
        "id": "stable-kebab-id",
        "kind": "dimension" | "datum",
        "label": "what this marks",
        "from": [0,0,0], "to": [0,0,0],
        "value": 42.0, "unit": "mm",
        "tolerance": "ONLY from a drawing or specification — see below",
        "how": "observed" | "retrieved" | "calculated" | "inferred" | "assumed" | "proposed",
        "source": "where it came from",
        "note": "what a reader needs alongside the number"
      }
    ]
  },
  "proposed_goal": null or {
    "title": "short title",
    "statement": "what you would actually do, in full",
    "risk_tier": "r1"
  }
}

About "prototype_edit" — CHANGING the model already on screen:

When a model is on screen and they ask for a CHANGE to it, send "prototype_edit"
instead of "prototype". Never both.

    "prototype_edit": {
      "remove": {"parts": ["part-id"], "features": ["feature-id"]},
      "patch": {
        "parts": [ ...whole parts, by id... ],
        "features": [ ...whole features, by id... ],
        "parameters": [ ... ], "derived": [ ... ],
        "assumptions": ["what you chose for THIS change"],
        "not_verified": ["what this change does not establish"]
      }
    }

- A part or feature in "patch" whose id ALREADY EXISTS replaces that one whole.
  A new id is added. Anything you do not mention is left exactly as it is —
  which is the point: you cannot mistype a dimension you did not send.
- "remove" is how a part goes away. Leaving it out of a patch does NOT remove it.
  That is deliberate: an edit only says what changes, so an omission means "not
  changed" and never "delete". Naming something that is not there is an error.
- Removing a part that a feature uses breaks that feature. Remove the feature too.
- "assumptions" and "not_verified" in a patch are ADDED to what is already there.
  The earlier ones still hold; do not restate them.
- Send a whole "prototype" instead when there is nothing on screen yet, or when
  they are asking for a different design rather than a change to this one.

About "prototype":

- Positions are in the stated units, Y is up, and the origin is the assembly's
  centre. Parts are centred on their own position.
- Part ids are STABLE ACROSS TURNS. When you revise an assembly, the part that
  was "base-plate" stays "base-plate" — that is what lets somebody put the two
  versions side by side and see what changed rather than two unrelated designs.
  Reuse the id even when the dimensions change; use a new id only for a part
  that was not there before. RESHAPING A PART DOES NOT CHANGE ITS ID: a body
  that becomes an extrusion is still the same body and keeps the name it had.
- A REVISION KEEPS EVERY PART IT WAS NOT ASKED TO REMOVE. Asked to reshape the
  body, send back the wheels, the spoiler and everything else exactly as they
  were. Leaving a part out is how it is deleted, so a part you simply did not
  mention is a part you destroyed — and the person is told their body was
  reshaped, not that their spoiler is gone. If you find yourself rewriting the
  whole model to change one thing, send "prototype_edit" instead: what it does
  not mention cannot be lost.
- Only emit it when the shape is the point. Do not attach geometry to a
  conversation about scheduling.
- "assumptions" is where every dimension you CHOSE goes. If they said "a
  bracket" and you picked 60mm, that belongs there.
- A figure from a PUBLISHED STANDARD is not an assumption and does not belong in
  that list. "NEMA 17 is 42.3mm across the face" is a claim about the world, and
  you are recalling it, not reading it — there is no reference source in this
  deployment and nothing here can check you. Quote one only when it changes what
  you would build; otherwise do not quote a number at all. When you do, put it in
  "parameters" with "how": "standard" and name the source, because that is the
  one place it can be labelled as recalled and read back against the published
  figure. A wrong figure attached to a real standard is more dangerous than no
  figure, because it is specific enough to be acted on — and a wrong one sitting
  in a typed field beside a citation is more dangerous still, because it looks
  like provenance.
- "parameters" are EVERY fixed number this design rests on, each with a unit —
  both the ones somebody could change and the ones nobody can. A dimension you
  picked is a parameter with "how": "chosen". A figure you are recalling from a
  standard is a parameter too, with "how": "standard" and the source named; it
  goes here even though nobody may change it, because "how" is what tells the
  two apart and this is the only list that has it. Both lists are OPTIONAL —
  leave them out when the shape is not really parametric.
- "derived" is for anything whose correct value DEPENDS on another parameter.
  Write the relationship, never the number it currently works out to. A rib
  whose length does not follow the plate it sits on will hang off the edge the
  moment the plate shrinks, and that is the commonest way a model that looked
  right stops being buildable.
  EVERY expression here must name at least one parameter. A bare number in
  "derived" — "42.3", "31.0" — is always wrong, because nothing about it follows
  anything: it is a fixed number and belongs in "parameters", which is the only
  list that carries a unit and a source. A recalled standard figure put here
  instead arrives with neither, and nothing can check it.
- "size_from" and "position_from" are what make the parameters real. Each key
  names an expression over your parameters, and FORGE evaluates it and draws the
  result — so a plate whose width IS plate_size should say that, rather than
  repeat the number. Bind every dimension that follows from a parameter; a
  thickness you simply chose and that follows nothing needs no entry.
  "position_from" keys are "x", "y" and "z", and Y is up.
  Fill in "size" and "position" as well, with the values as they stand now. They
  are what gets drawn if an expression cannot be read, and FORGE tells the reader
  when your number and your own expression disagree — so a rib bound to
  plate_size - 2 * fillet_radius on a 60 mm plate should say 54, not 52.
  An expression may use + - * / ^, brackets, the other parameter names, the
  constant pi, and sqrt, abs, min, max, floor, ceil and round. There is no sine
  or cosine here: half the world writes them in degrees and half in radians, so
  carry an already-resolved length as a parameter instead.
- There is NO "tube" shape. A hollow tube is a cylinder with a cylinder cut from
  it when the bore runs straight, and an outline with a "holes" loop when the
  bore follows the part — which is the only one of the two that can turn a
  corner with a bend. Both are below. Naming a wall thickness in prose while
  drawing a solid cylinder is the one thing to avoid: it reads as a bored part
  and machines as a bar.
- Reach for the shape that describes the thing, not the one that is easiest to
  type. A box is a box; it is not a car body, a bracket, a hull or a housing.
  Asked for something whose real form is curved or tapering, a stack of boxes and
  cylinders is not a simplification of it — it is a different object, and calling
  it "low-poly" or "conceptual" in the note does not make the geometry say what
  you meant. Use "extrusion" for a constant section, "revolve" for anything
  turned, "sweep" for a section carried along a route, "loft" for a section that
  changes. Then cut, fillet and chamfer what is left.
- "extrusion" is the shape for anything that is not a box or a cylinder: an
  L-bracket, a T-section, a channel, a gusset, a triangular plate. Give it a
  "profile" — a closed outline of at least three points in the part's own XY
  plane — and a "depth" in "size", and it is swept along the part's local Z.
  The outline is closed for you; do not repeat the first point at the end.
  The points are LOCAL coordinates and are NOT re-centred, so the part's position
  places the outline's origin. That is on purpose: it means a hole you place
  against a corner you drew stays against it. The depth IS centred, like a box's
  height.
  AN OUTLINE IS DRAWN FACING YOU, AND THAT IS WHERE IT LANDS. Its own x is the
  assembly's X and its own y is the assembly's Y, and the shape then travels
  along Z — the extrusion's "depth", or the sweep's path. So whatever you draw
  WIDE in the outline comes out wide ACROSS the assembly, not along it.
  This is the one that goes wrong. A car is 1900 wide, 800 tall and 4500 long.
  Its side elevation — the roofline, the bonnet, the taper — is 4500 across the
  page, so drawing that as an outline makes a car 4500 WIDE and 1900 long: the
  length and the width swapped. The reply says the body was reshaped and the
  model on screen is a slab.
  When the drawing you want IS the side elevation, say which way it faces:
    - "extrusion": keep "depth" as the width it travels (1900) and give the part
      "rotation": [0, 90, 0], which turns the drawing to face along the car.
    - "sweep": send the PATH across the car instead of along it — from
      {"x": -950, "y": 0, "z": 0} to {"x": 950, "y": 0, "z": 0} — because the
      outline stands square to the path, so a path along X faces the outline
      down the length. A path along Z leaves it facing the wrong way.
  Before you send a reshaped part, check its three dimensions against the ones it
  had. A restyle is not a resize: if the part was 1900 x 800 x 4500 and what you
  have drawn is 4500 x 600 x 1900, you have turned it on its side.
  Prefer "x_from" and "y_from" with expressions over literal numbers, for the
  same reason every other dimension does — an outline whose points do not follow
  the parameters is a drawing that stops being true the first time somebody
  changes one.
  The outline must not cross itself. A void that runs all the way through in the
  same shape — a box section, a tube — is a "holes" loop; a drilled hole is a
  cut feature. See both below.
- "revolve" turns the same kind of outline about an axis instead of sweeping it:
  a shaft, a boss, a flange, a pulley, a dome, a nozzle. Give it a "profile" and
  an "axis" of "y" (up, the default) or "x". It needs no "depth" — a revolve's
  size is entirely its outline.
  Every point must be on ONE SIDE of that axis. Touching it is fine and usual —
  a dome's outline meets the axis at its apex — but an outline with points on
  both sides sweeps through itself and is not a solid.
  It always turns a full circle. For a sector, revolve the whole thing and cut
  away what you do not want, the same way a hole is a cut rather than a kind of
  part.
  A bead, a rounded rim or a filleted shoulder on a turned part is a "radius" on
  the outline point, because it goes all the way round with the outline.
- "sweep" carries the same kind of outline along a PATH instead of a straight
  line, which is where everything that BENDS comes from: a pipe run, a handrail,
  a cable tray, a wire form, a tube routed around something. Give it a "profile"
  — a CLOSED outline of at least THREE points, like an extrusion's — and a
  "path", which is an OPEN line of at least two points, with "x", "y" and "z" in
  the part's own frame. The two minimums are different because the two things
  are: an outline has to enclose an area, and a path only has to have a
  direction. A two-point profile is a line, it encloses nothing, and the part is
  left out of the model. It needs no "depth": the path says how far it goes.
  The outline's own origin RIDES the path and the outline starts square to the
  first segment, so a path of two points straight up local Z is exactly an
  extrusion. Draw the outline around (0, 0) when you want the path to run down
  the middle of the part, which is almost always what a pipe or a rail means.
  Nothing is centred: an extrusion centres its depth, but a path is drawn, and
  where you draw it is where the part goes.
  Corners are MITRED, like a fabricated bend. Two limits follow, and both are
  refused rather than guessed at: a path cannot turn back through 180°, and a
  bend cannot be tighter than the outline is wide — at a sharp corner the
  section on the inside would fold back through itself, so put the points
  further apart or draw a narrower outline.
  A bend is a "radius" on the path point that turns — see below. Without one the
  corner is MITRED, like a welded elbow rather than a bent tube, which is a
  different part and a different way of making it.
  "path_closed": true makes the path a LOOP — a ring, a hoop, a frame, a gasket,
  a roll bar. The last point joins the first, there are no ends and no caps, and
  the seam is a corner like any other, so it may have a bend radius too. Do not
  repeat the first point at the end; say "path_closed" instead.
  A closed path has to bring its section back to itself, and one that leaves a
  plane generally does not: carried round a loop the section comes back ROTATED,
  and the two ends would meet at an angle. A loop drawn in one plane always
  works. A three-dimensional one usually does not, and is refused with the angle
  named — so keep a loop flat unless you have a reason not to.
- "holes" are closed loops INSIDE the outline: the section's own voids, which
  follow it wherever it goes. Each is a list of points like the outline, in the
  same plane, and they may have corner radii too.
  A HOLE IN THE SECTION AND A HOLE THROUGH THE SOLID ARE DIFFERENT THINGS.
  A bolt hole through a plate is still a cylinder you place in space and "cut"
  with a feature — that is what a drill does, and it is the right way to say it.
  Use "holes" when the void follows the drawing: the bore of a tube that BENDS
  (which no cylinder can cut, because it turns the corner with the tube), a box
  section, a hollow extrusion, a groove that goes all the way round a revolve.
  Each hole must be wholly inside the outline and must not CROSS another one.
  A HOLE MUST LIE ENTIRELY INSIDE THE OUTLINE. A void that reaches the edge is
  not a hole — it is a bite taken out of the shape, and the OUTLINE has to go
  round it. If a loop's points sit outside the outline (below its lowest edge,
  past its end), you meant one of the two things below instead.
  Two overlapping holes are one hole, and have to be drawn as one loop.
  A hole INSIDE another hole is an ISLAND: solid material standing in the void,
  like the post in an annular slot, the bar of a letter A, or a lug in the bottom
  of a pocket. It keeps going — a hole inside an island is a bore through the
  post — so draw exactly the loops the shape has and the nesting says the rest.
- "via" on a point BENDS THE EDGE ARRIVING AT IT into a circular arc that passes
  through the via on the way. Use it for an edge that BOWS: a crescent, a lens, a
  cam lobe, a hook, a D-shaped shaft, the belly of a bracket that clears
  something. It works on an outline point, a hole point and a path point, and on
  a path it curves the run itself rather than only its corner.
  It is a POINT ON THE ARC, not a centre and not a direction. Three points fix a
  circle completely, so put the via roughly where the middle of the bulge should
  be and the arc follows.
  A "via" and a "radius" are different things and are not alternatives. A radius
  ROUNDS A CORNER between two straight edges; a via CURVES AN EDGE. A corner
  where an arc meets is left sharp — the radius there is ignored and reported —
  so do not put one on either end of a bowed edge.
  Two arcs between the same two points is a crescent, and that is a legitimate
  outline of TWO points: an outline needs three points only when every edge is
  straight.
  A via must not be in line with the two ends of its edge, or on top of one of
  them — there is no arc through three points in a row, and the edge is simply
  drawn straight and reported. It must not carry a radius, a z (except on a
  path), or a via of its own.
- "radius" on a point ROUNDS THAT CORNER: an arc of that radius, tangent to both
  edges meeting there. It works the same way on an outline point and on a path
  point, and on a path it is the BEND RADIUS — the number a tube bender is set
  to, and the thing that decides whether a tube survives being bent at all. Give
  a bent pipe or a formed bracket one; a sharp corner on something that is
  actually bent is a drawing of a part nobody can make.
  Use "radius_from" with an expression wherever the radius follows a parameter,
  for the same reason every other dimension does.
  Two radii on one edge must fit: each eats r × tan(half the turn) of the edge
  either side of it, so two big radii on a short edge are refused rather than
  guessed at, as is a radius where the edges either side are in line.
  Do not put one on the FIRST or LAST point of an open path: those are ends, not
  corners, and there is nothing there to round. It is ignored and reported rather
  than refused, but it is still a number that means nothing.
  A SLOT is a rectangle whose radius is half its width on all four corners: the
  arcs at each end meet and the straight between them disappears. A stadium, a
  racetrack, a rounded gusset and a D-section are all the same one number.
  This overlaps "fillet", and the difference is real: a radius is part of the
  DRAWING, so it follows the section round every bend of a sweep and all the way
  round a revolve; a fillet is an operation on the finished solid, chosen by
  rule. Prefer the radius when the shape simply has it.
  What it cannot say is an arc that does NOT meet its neighbours smoothly — a
  crescent, a lens, a bulged edge. There is no vocabulary for those here.
- "section" is a drawing with NO thickness, and it exists for one purpose: to be
  a station of a "loft". Give it a "profile" like an extrusion's; give it no
  depth. On its own it encloses nothing.
- "loft" blends one section into the next, in the order you name them, and is the
  shape for anything whose CROSS-SECTION CHANGES along its length: a hull, a
  fuselage, a car body, a turbine blade, a bottle, a duct that tapers. Nothing
  else in this vocabulary can say that — an extrusion carries one outline along a
  line, a revolve turns one about an axis, and a sweep carries one along a path,
  but all three move a SINGLE section unchanged.
  Stack the stations along local Z by their "position", because a section lies in
  its own XY plane: two stations offset along x or y are side by side in one
  plane with no length to blend through, and the kernel will refuse it.
  The stations are CONSUMED, like a cut's tool: they become the body and do not
  also appear as flat plates.
  A body that is genuinely a constant section is an extrusion — do not loft two
  identical stations to say what one extrusion says.
- "features" are what make an assembly a PART rather than a pile of solids.
  A HOLE is not a part — it is the absence of one. Put a cylinder where the hole
  goes, size and place it like any other part, and then "cut" it from the thing
  it passes through. The cylinder is CONSUMED: it becomes the void, and does not
  also appear as a solid.
  SO NEVER CUT WITH A PART YOU WANT TO KEEP. The tool stops being a body. If you
  cut a recess using the very part that sits in it, that part is destroyed and
  the assembly is left with the hollow and nothing in it. Make a SEPARATE cutter,
  a little larger than the thing that has to fit, and give it its own id.
  THIS IS ALSO HOW YOU REMOVE MATERIAL THAT IS NOT A HOLE. A recess, a pocket, a
  slot, a scallop, an arch over a wheel, a notch in an edge — make the VOID as an
  ordinary part shaped like the space you want gone (a cylinder for an arch or a
  bore, a box for a pocket or a slot), place it so it overlaps the material, and
  "cut" it. Reach for this before reshaping the body's own outline: it leaves the
  part you already had intact, and it works the same whether the void is inside
  the shape or open at its edge.
  Worked example — wheel arches in a car body. The body is a box and the four
  wheels are parts you are keeping. For each wheel add a NEW cylinder — its own
  id, a little larger than the tyre, on the same axis and in the same place — and
  "cut" those four from the body. The four new cylinders are consumed; the four
  wheels survive and now sit in the arches. Cutting with the wheels themselves
  would leave a body with four hollows and no wheels at all. And do NOT draw the
  arches as "holes" in a profile: an arch opens at the bottom edge of the body,
  so it is not inside the outline and is not a hole.
  "fuse" welds parts into one body. Say it only when they really are one piece;
  two parts touching are two parts, and fusing them is a claim about how the
  thing is made.
  "fillet" rounds edges and "chamfer" cuts them off. Both take a size — prefer
  "radius_from" with an expression, for the same reason every other dimension
  does — and choose edges by RULE: "vertical" is the up axis, "top" and "bottom"
  are the highest and lowest edges, "all" is everything. There is deliberately no
  way to name an edge by number, because an index picks a different edge as soon
  as a parameter changes.
  Features apply IN ORDER, so cutting the holes and then rounding what is left is
  a different part from rounding first.
  Only the CAD kernel performs these. The viewport draws solid primitives, so a
  cut hole is drawn as a part standing in the plate and the reader is told so —
  do not work around that by leaving the hole out.
- "material" is a CLAIM about what a part is made of, not a rendering hint.
  Cost, weight, whether it can be welded and whether it survives the load all
  follow from it. If they told you, label it "observed" and quote them; if you
  chose it because a bracket is usually aluminium, label it "assumed" — that is
  a real answer and it is shown as one. "finish" is only how it catches light:
  ` + geometry.FinishGuide() + `.
- "states" are named configurations: which parts are shown, and where they sit.
  A state with "offsets" says these pieces separate along this path, and
  NOTHING here checks that they can — there is no interference, clearance or
  kinematic test in this deployment. Offer states when somebody is asking how a
  thing goes together or comes apart; do not add an "exploded" state, the
  viewer already has a slider for that.
- "overlays" are engineering marks on the model: dimensions, datums and notes.
  A note is a comment pinned to a point — use it for something about ONE
  feature, not for the general remarks that belong in your reply. Leave it
  out unless somebody GAVE you a figure to mark. You do not need to dimension the
  overall size — FORGE measures the model's own extents itself and draws them,
  labelled as its own arithmetic, so repeating them here adds nothing.
- NEVER invent a TOLERANCE. Nothing about a shape implies one: it is a
  manufacturing decision about process and fit, and the same cylinder is ±0.5 or
  ±0.005 depending on what it does. A tolerance is read as an instruction to a
  machinist, so one you produced from reasoning is the most dangerous number you
  can emit here. Only mark a tolerance somebody gave you, label it "observed" or
  "retrieved", and name the drawing or specification it came from. Anything else
  is removed before the render is drawn and the reader is told you tried.
- "not_verified" is mandatory whenever geometry is present, and it must be
  specific. "Not stress-analysed" and "no interference check was run" are useful;
  "this is a concept" is not. There is no FEA or CAD kernel in this deployment,
  so nothing here has been checked against anything.

About "proposed_goal": offer one only when they have described work they want
DONE, not merely discussed. It is a proposal — nothing runs until they start it.`

// NotVerifiedFallback is what VIS-06's banner says when the model supplied
// nothing of its own.
//
// A named constant rather than a literal at its one call site, because a second
// reader needs it: the evaluation suite measures whether the MODEL wrote
// something specific, and it can only do that by telling the model's own words
// apart from this backstop (internal/eval/scorers.go). Two copies of this
// sentence would drift, and the drift would silently credit the backstop to the
// model — the property would stop being measured with nothing reporting it.
// Reworded in wave 14, when "there is no CAD kernel" stopped being true of every
// deployment. What it must say is what is still true of ALL of them: a kernel
// builds a solid and checks nothing about it, so the presence of one changes
// nothing about this sentence except the part that was about to become false.
const NotVerifiedFallback = "Nothing here has been analysed or checked. There is no solver " +
	"and no interference check in this deployment — this is a shape, not a result."

// Conversation is the workbench dialogue.
//
// It is deliberately separate from the planner and the executor. This surface
// reasons WITH someone; those two act. Keeping them apart means a conversation
// cannot accidentally start work, which is the property PRD AGT-04 is protecting
// when it says autonomy is never raised silently.
type Conversation struct {
	client llm.Client
	char   persona.Character
	// characters resolves the project's character (PRD RSN-04). Optional; nil
	// answers every project with the character this conversation was built with.
	characters *CharacterStore
	// domains resolves the project's industry, for its units and vocabulary
	// (PRD §"Domain packs"). Optional; nil answers every project with `general`,
	// which carries no conventions and so asserts nothing about a domain nobody
	// established.
	domains *DomainStore
}

// NewConversation returns the conversational surface.
func NewConversation(client llm.Client, char persona.Character) *Conversation {
	return &Conversation{client: client, char: char}
}

// WithCharacters makes conversation honour the project's critique intensity.
func (c *Conversation) WithCharacters(s *CharacterStore) *Conversation {
	c.characters = s
	return c
}

// WithDomains makes conversation answer in the project's industry — its units,
// its vocabulary, and what a first answer there is expected to establish.
func (c *Conversation) WithDomains(s *DomainStore) *Conversation {
	c.domains = s
	return c
}

// framingFor returns the conversation instruction for a domain.
//
// # Why the conventions go AFTER the framing rather than into it
//
// converseFraming is how FORGE talks — speech short, detail on screen, propose
// geometry for physical things. That is invariant. The pack adds what it must
// get right to be useful HERE: units, vocabulary, what a first answer has to
// establish. Two different kinds of instruction, so they are two blocks, and the
// domain one can be absent without leaving a hole in the other.
//
// `general` carries no conventions by definition — it is the unknown-domain
// pack — so an unstated industry gets exactly the framing this had before packs
// were read at all. That is the property that makes this safe to add: nothing
// changes for a project that never chose a domain.
func framingFor(domain domainpack.Definition) string {
	if strings.TrimSpace(domain.Conventions) == "" {
		return converseFraming
	}
	out := converseFraming + "\n\nThis project is " + domain.Industry + " work. " +
		"What that requires of an answer here:\n\n" + domain.Conventions
	// The frame every coordinate in this reply is read in. Stated separately from
	// the prose because it is the one line that makes a position mean anything,
	// and it DIFFERS between domains — a vehicle is X-forward, a building is Z-up
	// against a site datum. A number read in the wrong frame is wrong without
	// looking wrong.
	if strings.TrimSpace(domain.GeometryAxes) != "" {
		out += "\n\nCoordinates here: " + domain.GeometryAxes
		if domain.GeometryUnit != "" {
			out += " Default unit: " + domain.GeometryUnit + "."
		}
	}
	// What must not travel. The model is the thing that decides what goes into a
	// reply, so a handling rule that never reached it would be a rule addressed
	// to nobody.
	if strings.TrimSpace(domain.DataRules) != "" {
		out += "\n\nHandling material in this domain: " + domain.DataRules
	}
	return out + "\n\nThe boundary of this domain: " + domain.Summary
}

// Turn is one exchange, as stored and replayed.
type Turn struct {
	Role    string `json:"role"` // "user" | "forge"
	Content string `json:"content"`
}

// HistoryContent renders one of FORGE's replies as a LATER turn should see it.
//
// # Why both halves reach the next turn
//
// A reply has two: the speech, kept short because it is spoken aloud, and the
// detail, which is where the reasoning goes because the screen can carry it
// (PRD §5.3). Only the speech used to travel, so FORGE could explain a choice at
// length and then, one question later, have no idea why it had chosen it. "Why
// did you say 3mm?" — the most ordinary follow-up there is — was the one thing
// it could not answer about its own answer.
//
// # Why they stay labelled instead of running together
//
// The label is load-bearing, not tidiness. What keeps FORGE's speech short is
// largely its sense of how it spoke last time, and a model whose own previous
// turns arrive as long paragraphs learns that long spoken replies are normal.
// The evaluation suite floors spoken replies at 70 words
// (internal/eval/scorers.go); saying which half was spoken is what keeps that
// floor measuring the same thing after the detail started travelling with it.
//
// ONE producer, called by the workbench (from its record) and by the evaluation
// harness (from the reply it just received). An eval that assembles a turn's
// history differently from the product is measuring a different system.
func HistoryContent(speech, detail string) string {
	speech = strings.TrimSpace(speech)
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return speech
	}
	shown := "[Shown on screen with that reply, not spoken aloud: " +
		clipRunes(detail, historyDetailLimit) + "]"
	if speech == "" {
		return shown
	}
	return speech + "\n\n" + shown
}

// historyDetailLimit bounds ONE recorded detail inside a later request.
//
// HistoryWindow bounds how many turns come back; nothing bounded how LONG one of
// them could be, and the record is permanent now — so a single reply carrying a
// long table would ride along in every request for the rest of the conversation.
// Two thousand characters is longer than any detail observed here and short
// enough that a full window of them is a few thousand tokens rather than a
// request the provider refuses.
const historyDetailLimit = 2000

// clipRunes shortens a recorded detail and says it did.
//
// The rule — count characters, cut on a boundary, mark the cut — is
// platform/text.Clip, because it was written here and separately in the ledger
// and one of the two got it wrong. What is local is WHY this caller wants it: a
// detail that quietly loses its ending leaves FORGE reasoning from part of its
// own argument with nothing to indicate the rest existed.
func clipRunes(s string, limit int) string { return text.Clip(s, limit) }

// HistoryWindow is how many earlier turns a conversation carries into a request.
//
// # Why it is exported
//
// The workbench reads a conversation's history out of its own record and hands
// it here, so two places now decide how far back a turn can see. If they
// disagreed, the caller would read a hundred turns for buildMessages to throw
// most of them away — and, worse, would believe it had passed a complete context
// while the model saw a fraction of it. One constant, read by both.
//
// Sixteen is a working figure rather than a measured one. It is enough to hold a
// design discussion together and short enough that a long session does not spend
// its budget re-reading itself.
const HistoryWindow = 16

// Prototype is a proposed 3D form.
//
// An ALIAS rather than a struct of its own: the shape the model emits and the
// shape that gets stored as a variant (PRD VIS-04) must be the same type, or the
// two definitions drift and a replayed render stops matching what was saved.
// It lives in internal/domain/geometry because a domain package cannot import
// the agent to find out what a part is.
type Prototype = geometry.Document

// PrototypePart is one solid.
type PrototypePart = geometry.Part

// noteRepair appends to what the reader is told about corrections to this reply.
//
// The same channel a misread dimension uses, for the same reason: a reply that
// was adjusted on its way out is not the reply the model wrote, and somebody
// reading it is entitled to know which.
func (r *Reply) noteRepair(note string) {
	if r.Repaired != "" {
		r.Repaired += " " + note
		return
	}
	r.Repaired = note
}

// resolveEdit turns an edit into the document it describes.
//
// # Why it happens here and not downstream
//
// An edit is an input convenience. Everything after this point already consumes
// a whole Document, so resolving it at the door means storage, rendering,
// comparison and export are untouched by this feature — which is what keeps a
// change to how FORGE is ASKED from becoming a change to what FORGE IS.
//
// # The three refusals
//
// Each is a case where continuing would produce a version that looks deliberate
// and is not:
//
//   - Both forms at once. "Here is the whole model" and "here is a change to it"
//     cannot both be authoritative, and picking one would be a guess about which
//     the agent meant.
//   - An edit with nothing to edit. The first turn of a project has no model on
//     screen; applying a patch to nothing would invent a design from a fragment.
//   - An edit that changes nothing. A version recording a change nobody made is
//     a false entry in the history somebody will later try to interpret.
func (r *Reply) resolveEdit(current *Prototype) error {
	const op = "agent.Reply.resolveEdit"
	if r == nil || r.PrototypeEdit == nil {
		return nil
	}
	edit := *r.PrototypeEdit
	r.PrototypeEdit = nil // consumed, whatever happens next

	// ‼️ Both forms at once was a REFUSAL, and refusing lost the whole turn.
	//
	// The prompt says "never both". Measured on a real turn (2026-09-09, "add
	// wheel wells"): the model sent both anyway, the turn was refused, and the
	// person got "I've added wheel arches to the body" with NO GEOMETRY. That is
	// strictly worse than either reading of an ambiguous reply — the speech had
	// already told them the work was done.
	//
	// So one is chosen, and which one is not arbitrary. The EDIT wins whenever
	// there is a model on screen to apply it to, because it is the form that
	// cannot drift: parts it does not mention are not in the payload, so they
	// come back exactly as they were. Taking the whole prototype would put every
	// untouched dimension back through the model's typing, which is the failure
	// this entire shape exists to prevent.
	//
	// With nothing on screen there is no base to apply an edit to, so the whole
	// prototype is the only thing that can be used.
	if r.Prototype != nil {
		if current == nil || len(current.Parts) == 0 {
			r.PrototypeEdit = nil
			r.noteRepair("This reply carried both a whole model and an edit to one. There was " +
				"nothing on screen to edit, so the whole model was used.")
			return nil
		}
		r.Prototype = nil // the edit is applied below, to the model on screen
		r.noteRepair("This reply carried both a whole model and an edit to one. The edit was " +
			"applied, because it leaves every part it does not mention exactly as it was.")
	}
	if current == nil || len(current.Parts) == 0 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("this reply edits the model on screen, and there is no model on screen. " +
				"Send a whole prototype for the first shape in a project")
	}
	if edit.Empty() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("this edit changes nothing. Say in words that nothing changed rather " +
				"than recording a version that did not")
	}

	applied, problems := edit.Apply(*current)
	if len(problems) > 0 {
		details := make([]string, 0, len(problems))
		for _, p := range problems {
			details = append(details, p.Detail)
		}
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("this edit could not be applied to the model on screen: %s",
				strings.Join(details, "; "))
	}
	r.Prototype = &applied
	return nil
}

// ProposedGoal is work FORGE offers to do. Nothing runs until a human starts it.
type ProposedGoal struct {
	Title     string `json:"title"`
	Statement string `json:"statement"`
	RiskTier  string `json:"risk_tier"`
}

// Reply is one response.
type Reply struct {
	Speech    string     `json:"speech"`
	Detail    string     `json:"detail"`
	Prototype *Prototype `json:"prototype"`
	// PrototypeEdit changes the model already on screen instead of restating it.
	//
	// Resolved into Prototype before this reply leaves the agent, so nothing
	// downstream — the viewport, the store, compare, export, the CAD kernel —
	// ever sees an edit. They all consume a whole Document and none of them
	// should learn a second shape. See resolveEdit and geometry/edit.go.
	PrototypeEdit *geometry.Edit `json:"prototype_edit,omitempty"`
	ProposedGoal  *ProposedGoal  `json:"proposed_goal"`
	// Claims is the epistemic ledger (PRD RSN-05): every statement in this reply
	// together with how FORGE came to hold it. Derived from the reply, never
	// asked of the model — see ClaimLedger.
	Claims []Claim `json:"claims,omitempty"`
	// Recalled is computed from the reply's own text, never asked of the model.
	// A component cannot be its own guard: the failure being caught here is the
	// model stating a standard's figure it has no way to check, and asking it to
	// self-report that is asking it to notice the thing it just failed to
	// notice. See standards.go.
	Recalled []StandardsClaim `json:"recalled,omitempty"`
	Model    string           `json:"model"`
	Usage    llm.Usage        `json:"-"`
	// LatencyMS is measured client-of-the-provider side and reported to the UI,
	// which displays the REAL figure rather than claiming the PRD's ≤700ms
	// target. A target asserted without measurement is a marketing claim.
	LatencyMS int64 `json:"latency_ms"`
	// Repaired says the reply did not parse as sent and what was read to save
	// it. Empty for the ordinary case, which is almost every reply.
	//
	// It is a FIELD and not a log line because the person is owed it: the
	// document they are looking at is not byte-for-byte the one the model
	// produced, and everything else in this system that substitutes something
	// says so on the screen. See dimensionrepair.go.
	Repaired string `json:"repaired,omitempty"`
}

// Respond produces one turn of conversation.
//
// projectID selects the character to answer in (PRD RSN-04). Empty is legal and
// means the constructed default — the evaluation harness has no project, and a
// deployment that never sets a character never needs one.
//
// images are data URIs attached to this turn — a sketch, a photograph of a
// part, a screenshot of a drawing (PRD VIS-01). They route the turn to the
// vision model, and a deployment that has not configured one refuses rather
// than sending a picture to a model that cannot see.
func (c *Conversation) Respond(ctx context.Context, projectID string, history []Turn, message string, workspaceNote string, current *Prototype, images []string) (*Reply, error) {
	const op = "agent.Conversation.Respond"

	if strings.TrimSpace(message) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).WithDetail("empty message")
	}
	role := llm.RoleConverse
	if len(images) > 0 {
		// The same discipline as a missing CAD kernel (tools/unavailable.go).
		// Most text models do not fail on an image — they describe their own
		// confusion in confident prose, which is exactly the answer this product
		// exists not to produce. So the absence is reported, never worked around.
		if c.client.ModelFor(llm.RoleVision) == "" {
			return nil, errs.New(op, errs.CodeConnectorUnavailable).
				WithDetail("this deployment has no vision model configured, so FORGE cannot look at " +
					"the image. Set FORGE_LLM_VISION_MODEL to a model that reads images. It is " +
					"deliberately unset by default: a text model sent a picture does not refuse, it " +
					"describes what it imagines, and that answer is indistinguishable from one it saw.")
		}
		role = llm.RoleVision
	}

	// One builder, not two. buildMessages has always said it was "shared by both
	// paths" and this function had its own copy of it — so the streaming path
	// and the buffered path assembled the same request separately, and an image
	// added to one would simply not exist in the other.
	messages := c.buildMessages(c.characters.For(ctx, projectID, c.char),
		c.domains.For(ctx, projectID), history, message, workspaceNote, current, images)

	resp, err := c.client.Complete(ctx, llm.Request{
		Role:      role,
		Messages:  messages,
		JSONMode:  true,
		MaxTokens: converseMaxTokens,
	})
	if err != nil {
		return nil, err
	}

	var reply Reply
	body := []byte(extractJSON(resp.Content))
	if err := json.Unmarshal(body, &reply); err != nil {
		// An expression written where a number was expected is the commonest
		// way a complete, correct reply fails to parse — the contract offers
		// two slots per dimension and tells the model to prefer the one it then
		// puts in the wrong place. Read rather than lost; see
		// dimensionrepair.go for the measurement and the reasoning.
		//
		// Only after the strict parse has already failed, so a reply that
		// parses is never rewritten.
		repaired, moved := repairDimensions(body)
		if moved {
			var second Reply
			if err2 := json.Unmarshal(repaired, &second); err2 == nil {
				reply = second
				// Said out loud, in the channel the person already reads for
				// what FORGE assumed. A document silently different from the
				// one the model sent is the same class of thing as a render
				// that does not match its file.
				reply.Repaired = "One or more dimensions arrived as expressions written in the " +
					"place of a number. They were read as the expressions they are — the " +
					"contract has a field for each — rather than the reply being discarded."
				err = nil
			}
		}
		if err != nil {
			reply = Reply{Speech: unreadableReply(resp)}
			if reply.Speech == "" {
				return nil, errs.Wrap(op, errs.CodeExternalProtocol, err).
					WithDetail("the model returned neither usable JSON nor any text")
			}
		}
	}
	reply.Model = resp.Model
	reply.Usage = resp.Usage
	reply.LatencyMS = resp.Latency.Milliseconds()

	if err := reply.validate(); err != nil {
		return nil, err
	}
	// The same resolution the streamed path does, at the same point. A rule
	// enforced in one of two paths holds until somebody uses the other one —
	// which is the reason validate() itself lives at this choke point.
	if err := reply.resolveEdit(current); err != nil {
		return nil, err
	}
	// The same repair the streamed path does, at the same point.
	c.repairIfFaulty(ctx, &reply)
	c.repairIfTurned(ctx, &reply, current)
	c.repairIfItLooksWrong(ctx, &reply, message)
	noteVanished(&reply, current)
	return &reply, nil
}

// validate enforces the honesty rules on a reply.
//
// It is the single choke point both the buffered and the streamed path go
// through, which is why the standards scan lives here rather than in either
// caller: a rule enforced in one of two paths is a rule that holds until someone
// uses the other one.
func (r *Reply) validate() error {
	const op = "agent.Reply.validate"

	if strings.TrimSpace(r.Speech) == "" && strings.TrimSpace(r.Detail) == "" {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the reply carried nothing to say or show")
	}

	// Computed before the prototype fix-ups below, so a claim is found in the
	// text the model actually produced.
	r.Recalled = FindStandardsClaims(r)
	r.Claims = r.ClaimLedger()
	if r.Prototype != nil {
		if len(r.Prototype.Parts) == 0 {
			// An empty prototype renders as a blank viewport, which reads as a
			// failure. Dropping it is more honest than showing nothing.
			r.Prototype = nil
			return nil
		}
		/* PRD WRK-05: a dimension without its unit will eventually be read in
		 * the wrong one.
		 *
		 * The units field is free text from a model, so it can be missing,
		 * misspelled, or something we cannot convert. An unrecognised unit is NOT
		 * quietly treated as millimetres — a wrong guess about scale is the
		 * difference between a bracket and a building. It is recorded as
		 * unspecified, every dimension then renders as "60 (unit not stated)",
		 * and the reader is told in the one place they are already looking. */
		if _, known := geometry.ParseUnit(r.Prototype.Units); !known && len(r.Prototype.Parts) > 0 {
			declared := strings.TrimSpace(r.Prototype.Units)
			note := "No unit was stated for these dimensions, so every number here is unitless."
			if declared != "" {
				note = fmt.Sprintf("The unit %q is not one FORGE can convert, so every number here is unitless.", declared)
			}
			r.Prototype.Units = ""
			r.Prototype.NotVerified = append(r.Prototype.NotVerified, note)
		}
		// PRD VIS-03. Overlays arrive from the model like everything else here,
		// and a dimension line with a tolerance on it is the most authoritative
		// mark that can appear on a render. The storage door refuses a bad one
		// outright; this door drops it and says so, because refusing the whole
		// turn would throw away the shape somebody is waiting on — the same
		// treatment the unrecognised unit gets above.
		//
		// Appended to NotVerified rather than logged, because that is the one
		// place the reader is already looking, and "FORGE tried to state a
		// tolerance and it was removed" is exactly what they need to know about
		// what is in front of them.
		if len(r.Prototype.Overlays) > 0 {
			kept, dropped := geometry.DrawableOverlays(r.Prototype.Overlays)
			r.Prototype.Overlays = kept
			r.Prototype.NotVerified = append(r.Prototype.NotVerified, dropped...)
		}
		// PRD VIS-02. Materials and states arrive from the model like everything
		// else here. A material with an unusable finish keeps its NAME and loses
		// its look — the name is the claim — and a state referring to a part
		// that does not exist is dropped, because the viewer would show the
		// assembly unchanged and a reader would take that for the state making
		// no difference.
		for i := range r.Prototype.Parts {
			if m := r.Prototype.Parts[i].Material; m != nil {
				if err := m.Validate(); err != nil {
					r.Prototype.Parts[i].Material = nil
					r.Prototype.NotVerified = append(r.Prototype.NotVerified,
						"A material FORGE named could not be read and was dropped: "+err.Error())
				}
			}
		}
		if err := geometry.ValidateStates(r.Prototype.States, r.Prototype.Parts); err != nil {
			r.Prototype.States = nil
			r.Prototype.NotVerified = append(r.Prototype.NotVerified,
				"The assembly states FORGE proposed referred to parts that are not in this "+
					"assembly, so none of them is shown. "+err.Error())
		}
		if note := geometry.StatesNotVerified(r.Prototype.States); note != "" {
			r.Prototype.NotVerified = append(r.Prototype.NotVerified, note)
		}
		// PRD VIS-06 as an invariant rather than an instruction: geometry
		// without a statement of what it does not establish is exactly the
		// render that gets mistaken for an analysis.
		if len(r.Prototype.NotVerified) == 0 {
			r.Prototype.NotVerified = []string{NotVerifiedFallback}
		}
		for i := range r.Prototype.Parts {
			p := &r.Prototype.Parts[i]
			if p.ID == "" {
				p.ID = fmt.Sprintf("part-%d", i+1)
			}
			if len(p.Position) != 3 {
				p.Position = []float64{0, 0, 0}
			}
			if len(p.Rotation) != 3 {
				p.Rotation = []float64{0, 0, 0}
			}
			if p.Opacity <= 0 || p.Opacity > 1 {
				p.Opacity = 1
			}
			if p.Color == "" {
				p.Color = "#b8bcc4"
			}
		}
		/* The parametric model, resolved and APPLIED (waves 10 and 11).
		 *
		 * Bind evaluates the document's expressions and writes the results into
		 * the numbers the renderer reads, so a part whose width follows
		 * plate_size actually follows it. It returns everything Resolve would
		 * have reported plus anything wrong with the bindings themselves, which
		 * is why there is one call here and not two.
		 *
		 * It runs LAST in this block because it needs what the loop above
		 * guarantees: every part has an id (Bind names parts by their label) and
		 * a three-element position to write an axis into.
		 *
		 * None of what it reports changes a pixel — a document whose parameters
		 * do not resolve renders exactly like one whose parameters do — which is
		 * precisely why it has to be said. Appended to NotVerified for the same
		 * reason as the dropped tolerances and the unconvertible unit above: it
		 * is the one place the reader is already looking. */
		for _, problem := range r.Prototype.Bind() {
			r.Prototype.NotVerified = append(r.Prototype.NotVerified, parameterNote(problem))
		}
		/* Features, and the one place the picture and the file disagree.
		 *
		 * A feature that does not check out is dropped by the kernel rather than
		 * approximated, so the reader has to be told which — an assembly missing
		 * a hole somebody asked for is not something the render will show.
		 *
		 * And the viewport has no boolean operations, so it cannot make the
		 * void. It draws the tool as a faint ghost rather than as a solid post
		 * — which is the opposite of what a hole is — and says so here. A real
		 * divergence between two things this product shows the same person,
		 * stated for the same reason "Drawn approximately" is. */
		/* An outline nothing could read is a part that is simply NOT THERE, and
		 * the render looks like a design with a piece missing rather than like
		 * an error. Its own voice, because "a number is missing" and "a whole
		 * part is absent" are different things to be told. */
		for _, problem := range r.Prototype.ProfileProblems() {
			r.Prototype.NotVerified = append(r.Prototype.NotVerified, profileNote(problem))
		}
		if _, featureProblems := r.Prototype.Operations(); len(featureProblems) > 0 {
			for _, problem := range featureProblems {
				r.Prototype.NotVerified = append(r.Prototype.NotVerified, featureNote(problem))
			}
		}
		r.Prototype.NotVerified = append(r.Prototype.NotVerified, r.Prototype.FeatureNotes()...)
	}
	return nil
}

// elapsedMS reports milliseconds since the turn's start instant, which is
// carried on the context by the HTTP layer.
//
// On the context rather than a field because a Conversation is shared across
// concurrent turns; a start time stored on the struct would be whichever
// request wrote it last.
func elapsedMS(ctx context.Context) int64 {
	if start, ok := ctx.Value(turnStartKey{}).(time.Time); ok {
		return time.Since(start).Milliseconds()
	}
	return 0
}

type turnStartKey struct{}

// WithTurnStart marks when a conversational turn began, so latency is measured
// from the moment the person finished speaking rather than from the model call.
func WithTurnStart(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, turnStartKey{}, at)
}

// converseMaxTokens bounds one conversational reply.
//
// # Why it is not 6000 any more, stated honestly
//
// It was 6000 while a prototype was a bag of primitives. The contract has since
// grown by five things a document can carry — parameters, derived expressions,
// bound dimensions, outlines and features — and each lengthens the reply for
// exactly the designs that need them most.
//
// This is a PRECAUTION and not a measurement. A V-belt pulley failed to parse on
// 2026-09-05 and truncation was the first suspicion; three further runs came
// back at about 1300 tokens with finish_reason "stop", so the cap was not what
// broke it — that reply was simply malformed, intermittently. Raising the
// headroom is still worth having, because the cost is nothing and the failure it
// would cause is the loss of a whole document, but nobody has seen that failure
// and this comment must not claim otherwise.
//
// What DID come out of that investigation is unreadableReply below, which is the
// real fix for what the person saw.
const converseMaxTokens = 16000

// unreadableReply is what to say when the model's JSON could not be read.
//
// # Why the raw text is not always spoken
//
// Falling back to speaking the raw content is right when the model answered in
// PROSE — a conversation that dies on a formatting slip is worse than one that
// occasionally speaks something unstructured, and the person is mid-sentence.
//
// It is wrong when the content is a half-written JSON object. That is not
// "something unstructured", it is the machinery, and printing it tells the
// reader nothing except that something broke in a way nobody will describe. A
// truncated reply is the commonest cause and it has a name, so it gets said.
func unreadableReply(resp *llm.Response) string {
	raw := strings.TrimSpace(resp.Content)
	if resp.Truncated() {
		return "That answer was cut off before it finished — the design was longer than one " +
			"reply can hold. Ask for it in pieces, or for fewer parts at a time, and it will " +
			"come back whole."
	}
	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		return "That reply came back in a shape FORGE could not read, so nothing of it is " +
			"shown rather than showing you the machinery. Asking again usually settles it."
	}
	return raw
}

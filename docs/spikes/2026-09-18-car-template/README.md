# The car template (looks-designed stage C), 2026-09-18

Measurements behind `internal/domain/geometry/car.go`, `carproportions.go`, `designwords.go` and the two
tables in `internal/domain/geometry/templates/`. Everything here ran offline, with no model call, on build123d
0.11.1 (`jarvis-k2b/.cadvenv`). The laptop was shared with other agents' builds, so every time below is one
contended run. None of them is a speed claim; the fence is the kernel's own 30 s build limit.

## 1. A fillet feature on the lofted body fails, so the sections carry the radius

`fillet_on_loft.py` lofts eight rounded-rectangle stations (a 4.5 m car), cuts four arch cylinders, and fillets
every edge of the body before and after the cut. Log: `fillet_on_loft.log`.

| radius | before the arches | after the arches |
|---|---|---|
| 5 mm | failed (`Failed creating a fillet with radius of 5`) | failed |
| 15 mm | failed | failed |
| 30 mm | the run hit its 900 s timeout during this attempt | not reached |

So the template never asks OCCT to fillet the lofted B-spline body. It puts a corner radius on every station
corner (`edge_radius`, a share of height), and the loft through rounded sections has rounded edges. The fillet
op is still used where it works: the splitter's edges (a box).

## 2. Eight stations at the cabin's corners make the smooth loft overshoot

The first body put eight stations at the nose, bonnet, windscreen base, roof front, roof rear, rear window base,
deck and tail (`stations-8.json`, dumped from that version). OCCT's smooth loft through them overshot the stated
roof height. The fix samples one C1 side view (smoothsteps for the bonnet, windscreen, rear window and tail) at
N evenly spaced stations. `overshoot.py` measures the first version and `smooth.py` the second. Log: `overshoot.log`.

| fixture | 8 corner stations | C1 view, N=8 | N=13 | **N=17 (shipped)** | N=25 |
|---|---|---|---|---|---|
| default | +13.0% | +2.8% | +1.2% | **+0.7%** | +0.5% |
| long-hood-gt | +61.3% | +1.9% | +0.0% | **+0.9%** | +0.2% |
| long-low-hypercar | +34.1% | +0.8% | +4.3% | **+1.2%** | +0.5% |
| short-tall-suv | +13.8% (and +9.2% wider) | +1.9% | +0.5% | **+0.7%** | +0.3% |

The overshoot came from the station layout, not the station count: eight samples of the C1 view are already
within 2.8%. 17 was chosen because it stays within 1.2% on every fixture while the loft still takes about 1 s.
`TestKernel_BuildsTheCarTemplateWithNothingMissingOrBuried` holds each built car to within 2% of its stated
height and 0.1% of its stated length.

## 3. Interference checks on every wheel nut put a car past the 30 s limit

The first kernel run (8-station body, a nut per lug) spent this long in the interference phase (from
`Build.Phases`):

| fixture | interference phase | whole build |
|---|---|---|
| default | 25.9 s | over the 30 s limit |
| long-low-hypercar | 30.6 s | over the limit |
| short-tall-suv | 10.1 s | 26 s |

The body's box contains every wheel part, so every nut cost a boolean against a B-spline body. The template now
fuses each wheel's nuts to its rim (a `fuse` on the wheel assembly). Final run, 17-station body, nuts fused,
kernel limit 30 s as in production:

| fixture | solids | interferences (buried) | height asked / built | features | interference | export |
|---|---|---|---|---|---|---|
| default | 14 | 0 (0) | 1250 / 1259 | 2.8 s | 5.5 s | 2.1 s |
| long-hood-gt | 14 | 0 (0) | 1300 / 1312 | 2.2 s | 5.6 s | 1.0 s |
| long-low-hypercar | 19 | 0 (0) | 1000 / 1012 | 2.5 s | 6.3 s | 1.2 s |
| short-tall-suv | 12 | 0 (0) | 1950 / 1963 | 1.7 s | 3.9 s | 1.6 s |

**Confound:** the second run changed two things at once (the station layout and the fused nuts), so the fall in
interference time is not split between them. What is structural: the nut pairs are gone (5 to 10 per wheel).

## 4. What each item became

- **C1**: a part with `"shape": "car"` and its numbers is written out at the settle door
  (`settleDocument` → `geometry.ExpandTemplates`) into parameters, derived values, definitions, three assemblies
  (car, body, wheel), a polar pattern of nuts, a linear pattern of diffuser fins, and assembly features (a loft
  through 17 sections, one cut of four arches, a fuse of nuts to rim, a fillet on the splitter). Every coordinate
  is an expression over the car's parameters, so a respec moves wheels, arches and body together
  (`TestCar_ARespecMovesTheWheelsTheArchesAndTheBodyTogether`). The model picks it the way it picks `"gear"`:
  from the shape list, with a paragraph whose example is built by a fence.
- **Bulged stations**: `stationCorner.Bulge` is the one slot. It is zero today and `carStationProfile` refuses any
  other value. When `looks/spline-outlines` lands, that function writes a non-zero bulge as the branch's curved
  segment and the side view sets bulges on the edges over the wheels. Nothing else changes: a station is still a
  `"section"` part.
- **C2**: `templates/car_proportions.json` holds eight cars, two per class, with a source and retrieval on each
  figure. It is labelled UNVALIDATED. Ranges are computed from the cars and widened by a 10% tolerance, which the
  file marks as FORGE's own number. Figures marked `"retrieved": "search"` were quoted by a web-search result
  whose page returned HTTP 403 and could not be opened. Gaps: no cited track or ground clearance for either
  hypercar, so those two rules are not checked for that class. Only the BMW and Defender sheets give both
  overhangs, plus the Corvette's (search-quoted).
- **C3**: `templates/design_words.json`, rendered by `DesignWordGuide` into the car paragraph of the prompt. A row
  may move a style knob inside the template's range, or lean a proportion toward one end of its class range. A
  row may never set a count.
- **C4**: no code change, on purpose. `sketch.go` turns the reference drawing into number-free words about form
  (`withoutNumbers`). Those words already reach the prompt that writes the car, next to the design-word table, so
  "wedge-shaped" or "fastback" in a drawing moves the style knobs through the same rows a person's words do, and
  a number in the drawing cannot reach anything. The fence is that every design word survives `withoutNumbers`
  unchanged. A more direct path (a closed "which silhouette is this" question in `sketchReadSystem`, or letting
  the sketch-match repair move stations) would add styling to `sketch.go`, which damon's 2026-09-18 decision
  keeps closed. So it was not built.

## Screenshots: not committed

The four fixtures were built and tessellated by the real kernel, with no skipped parts and no feature failures
(default: 75,624 triangles; long-low-hypercar: 85,064; short-tall-suv: 164,396; long-hood-gt: 87,514). The
payloads were loaded into `Forge3D.Studio` on a static page in the Browser pane. One dark-theme view of the
default car was seen in the pane: a lofted body with the arches cut, wheels, and nuts on the rims. It was not
saved to a file, because the route for saving the canvas failed to open and the task was told to skip
screenshots rather than block on them. No light-theme view and no before/after pair was taken. This stage does
not change the renderer.

## Reproduce

```
export FORGE_CAD_PYTHON=.../.cadvenv/Scripts/python.exe GOWORK=off
go test ./internal/domain/geometry/ -run 'TestCar|TestDesignWords' -count=1
go test ./internal/agent/ -run 'TestTheContractCarriesTheCar|TestSettle_WritesACar|TestDesignWordsSurvive' -count=1
go test ./internal/domain/cad/ -run TestKernel_BuildsTheCarTemplate -count=1 -v
python fillet_on_loft.py; python overshoot.py stations-8.json smooth; python smooth.py 17
```

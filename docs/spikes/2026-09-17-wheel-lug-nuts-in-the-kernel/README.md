# A wheel with lug nuts on a polar pattern, attached across assemblies, in the kernel

2026-09-17, branch `geometry/remaining`. Offline: no model calls. #118's live car never
reached a pattern, so the contract's polar-pattern and cylinder-axis sentences and
"are lug nuts buried" were untested in OCCT.

## Fixture

`internal/agent/car_wheel_kernel_test.go`. `carOnSuspension` (suspension-left at
(-640, 330, 1350), suspension-right mirrored across x) plus a hub on each corner (a
cylinder r 70 h 40 turned [0, 0, 90], outer face at x = -190 in the suspension frame)
and the `hub` interface at x = -290. A scripted wheels build step, written as the
contract teaches:

- `rim`: cylinder r 230 h 200; `tyre`: revolve about y, section 230..330 x ±110;
- `lug-nut`: `ISO 4032 M12`, position [60, nutY, 0], pattern polar, 5, about y;
- `placements`: left-wheel at `suspension-left/hub`, right-wheel at
  `suspension-right/hub`, both rotation [0, 0, 90] (the #111 root placement path).

Built with `Kernel.BuildProperties` (build123d 0.11.1, jarvis-k2b .cadvenv, Windows).

## Results

| nutY | parts | skipped | pairs sharing material | buried |
|---|---|---|---|---|
| 105.4 (rim outer face + half the 10.8 mm nut) | 18 | 0 | 0 | 0 |
| 0 (ring at the wheel centre, as live run 2 wrote it) | 18 | 0 | 10 | 10 (every nut x its own rim, fraction 1.000, 1,808.9 mm3 each) |

At nutY = 105.4, per wheel (kernel centres of volume and boxes):

- rim box 200 x 460 x 460 with the 200 along X: the cylinder stands on its own Y and the
  placement's [0, 0, 90] lays it on the axle;
- five nuts at 72 degree steps on a radius-60.000000 ring round the wheel axis
  (y 330, z 1350), each 10.8 mm along X (the nut's own axis is the wheel's);
- left nuts span x -1040.8..-1030, right nuts 1030..1040.8: on the outboard rim face on
  both sides, so the mirror of suspension-right carries through the interface to the
  wheel attached at it;
- every placed part's kernel centre equals its Go `Expanded()` position within 1e-6 mm.

A first fixture had the hub overlapping the suspension arm (54,000 mm3, fraction 0.100,
each side) - a fixture mistake, fixed by moving the hub; noted because it shows the check
reports sub-buried contact correctly too.

## Conclusion

Nothing wrong found in expansion, the cylinder axis, standard-nut orientation, mirroring
through an interface, or the buried-clash count. Live run 2's buried nuts were the reply's
placement (ring at the wheel centre), which the kernel reports as 10 buried pairs named
nut x rim - the input a repair needs. Kept as fences
`TestKernelCar_LugNutsOnAPolarPatternAcrossAssembliesSitOnTheRimFace` and
`TestKernelCar_LugNutsRingedAtTheWheelsCentreAreReportedBuried` (skip without
`FORGE_CAD_PYTHON`).

Not established: whether a live model now writes the ring at the face (needs a live run);
the step's own interference gate and repair on this fixture (built after the step, not
through `WithSolids`).

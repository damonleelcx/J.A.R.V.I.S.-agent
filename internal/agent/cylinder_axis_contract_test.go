package agent

import (
	"math"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// ‼️ The contract teaches a wheel's turn from the car's own forward axis, both ways
// (2026-09-17 live verification, follow-up 5).
//
// It used to give one example, a car laid out across X with its axles along X,
// turned [0, 0, 90]. Run 1's car drove along X, copied the [0, 0, 90] onto its rear
// hub carrier, and pointed the hub along the car; V4 flagged the axis. The rule is
// stated by the forward axis, and each of the two rotations is measured here against
// what FORGE draws, so the sentence cannot name a turn the drawing does not make.
// The RENDERED contract is read — what a turn and a build step are sent.
func TestTheContractTeachesAWheelsTurnFromTheCarsForwardAxis(t *testing.T) {
	for name, rendered := range map[string]string{"build": buildContract, "turn": converseFraming} {
		words := strings.Join(strings.Fields(rendered), " ")
		for _, want := range []string{
			`an axle that runs ACROSS the car, square to the car's own forward axis`,
			`a car whose forward axis is Z has its axles along X, and a wheel is turned "rotation": [0, 0, 90]`,
			`a car whose forward axis is X has its axles along Z, and a wheel is turned "rotation": [90, 0, 0]`,
			`Decide the forward axis first`,
		} {
			if !strings.Contains(words, want) {
				t.Errorf("the %s contract does not say %q", name, want)
			}
		}
		if strings.Contains(words, `an axle that runs across the model, along X, is turned`) {
			t.Errorf("the %s contract still teaches one layout's turn as the rule", name)
		}
	}
	wheel := func(rotation []float64) [3]float64 {
		return extentsOf(geometry.Document{Units: "mm", Parts: []geometry.Part{{ID: "wheel", Shape: "cylinder",
			Size: map[string]float64{"radius": 300, "height": 200}, Position: []float64{0, 0, 0}, Rotation: rotation}}})
	}
	// Forward along Z: the axle, and so the wheel's height, runs along X.
	if e := wheel([]float64{0, 0, 90}); math.Abs(e[0]-200) > 1 || math.Abs(e[2]-600) > 5 {
		t.Errorf("turned [0, 0, 90] a wheel is %.0f x %.0f x %.0f; the contract says its axis is X", e[0], e[1], e[2])
	}
	// Forward along X: the axle runs along Z.
	if e := wheel([]float64{90, 0, 0}); math.Abs(e[2]-200) > 1 || math.Abs(e[0]-600) > 5 {
		t.Errorf("turned [90, 0, 0] a wheel is %.0f x %.0f x %.0f; the contract says its axis is Z", e[0], e[1], e[2])
	}
}

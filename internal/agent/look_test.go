package agent

import "testing"

// The model answers in two shapes and both must survive.
//
// # What this closes
//
// The contract asks for {"part":…,"detail":…}. Measured 2026-09-09,
// qwen3.8-max answered {"problems":["Yes, the wheels are partially hidden
// inside the red body…"]} — plain strings. Read strictly that is an EMPTY
// problem list: a correct observation about a real defect, silently discarded,
// and reported to the reader as "nothing wrong". The most dangerous shape of
// bug in this file, because a check that fails quietly is trusted.
func TestLook_ReadsBothAnswerShapes(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"objects, as the contract asks",
			`{"problems":[{"part":"Front Left Wheel","detail":"is inside the body"}]}`,
			"Front Left Wheel: is inside the body"},
		{"bare strings, as the model actually answered",
			`{"problems":["Yes, the wheels are partially hidden inside the red body."]}`,
			"Yes, the wheels are partially hidden inside the red body."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLook(tc.body)
			if len(got) != 1 {
				t.Fatalf("read %d problems from %s", len(got), tc.body)
			}
			if got[0].Detail != tc.want {
				t.Errorf("got %q, want %q", got[0].Detail, tc.want)
			}
		})
	}
}

// An empty list is the ordinary answer and must stay empty.
func TestLook_EmptyIsEmpty(t *testing.T) {
	if got := parseLook(`{"problems":[]}`); len(got) != 0 {
		t.Errorf("an empty answer produced %d problems: %+v", len(got), got)
	}
	// And an entry with nothing in it is not a problem.
	if got := parseLook(`{"problems":[{"part":"","detail":""}]}`); len(got) != 0 {
		t.Errorf("an empty entry produced %d problems: %+v", len(got), got)
	}
}

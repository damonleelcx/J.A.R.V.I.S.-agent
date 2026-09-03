package media

// In package media because the barge-in decision is made by an unexported method
// on the packet path, and it is the half of AUD-01 that the room-level tests
// cannot reach: they can prove that Silence stops FORGE, but not that anything
// ever calls it.

import (
	"testing"

	"github.com/pion/rtp"
)

// withAudioLevel builds a packet carrying the RFC 6464 extension.
func withAudioLevel(t *testing.T, voice bool, level uint8) *rtp.Packet {
	t.Helper()
	ext := rtp.AudioLevelExtension{Level: level, Voice: voice}
	raw, err := ext.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	pkt := &rtp.Packet{Header: rtp.Header{Version: 2}}
	if err := pkt.Header.SetExtension(1, raw); err != nil {
		t.Fatal(err)
	}
	return pkt
}

// The two answers barge-in depends on, and both are load-bearing.
//
// # Why each direction is a real failure
//
// Always true: FORGE is interrupted by the first packet after it starts talking
// and can never complete a sentence. WebRTC clients transmit continuously
// through silence, so "a packet arrived" is true at all times — which is exactly
// why this reads the browser's voice-activity flag instead.
//
// Always false: barge-in never fires. FORGE talks over everybody, and AUD-01's
// "calm under interruption" becomes a claim with nothing behind it.
//
// A test asserting only one direction would pass on the opposite bug.
func TestBargeInReadsTheBrowsersVoiceActivityFlag(t *testing.T) {
	s := &SFU{}

	for _, tc := range []struct {
		name  string
		pkt   *rtp.Packet
		speak bool
		why   string
	}{
		{
			name:  "speech",
			pkt:   withAudioLevel(t, true, 40),
			speak: true,
			why:   "a packet the browser marked as speech must interrupt FORGE",
		},
		{
			name:  "silence, still transmitting",
			pkt:   withAudioLevel(t, false, 127),
			speak: false,
			why: "a packet the browser marked as silence must NOT interrupt. Clients send " +
				"continuously, so treating this as speech stops FORGE the instant it starts",
		},
		{
			name:  "loud, but marked as not speech",
			pkt:   withAudioLevel(t, false, 10),
			speak: false,
			why:   "the voice flag decides, not the level; a loud fan is not somebody talking",
		},
		{
			name:  "no extension negotiated",
			pkt:   &rtp.Packet{Header: rtp.Header{Version: 2}},
			speak: false,
			why: "with no flag to read the answer must be no. FORGE finishing its sentence " +
				"is a smaller harm than FORGE never being able to speak, and the explicit " +
				"stop control still works",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.speakingNow(tc.pkt); got != tc.speak {
				t.Errorf("speakingNow = %v, want %v — %s", got, tc.speak, tc.why)
			}
		})
	}
}

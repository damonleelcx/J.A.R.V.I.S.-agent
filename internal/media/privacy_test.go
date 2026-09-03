package media_test

import (
	"context"
	"testing"
	"time"

	forgemedia "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/media"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// AUD-07's controls and SEC-06's privacy state, enforced rather than displayed.
//
// Every test here checks what the SERVER does. A control that only stops the
// browser sending is a picture of a control: it is undone by a bug, a stale tab,
// or anybody who edits the page. The point of these is that the person relying
// on the control does not have to trust the software running on everybody else's
// machine.

// Muting stops the audio, at the server.
//
// The assertion is on what the OTHER participant receives, because that is the
// only thing that matters to the person who pressed mute. A test that asserted
// the state field had changed would pass on a build that faithfully recorded the
// mute and went on forwarding every packet.
func TestMutingStopsTheAudioAtTheServer(t *testing.T) {
	sfu, err := forgemedia.New(forgemedia.Options{
		Config: testConfig(5), Log: logx.Discard(), Clock: clock.System{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sfu.Close()
	sig := newSignalRecorder()
	sfu.SetSignaller(sig)
	const roomID = "rom_mute"

	alice := newClient(t, "stm_alice", "usr_alice")
	bob := newClient(t, "stm_bob", "usr_bob")
	sig.on("stm_alice", func(sdp string) { alice.answerRenegotiation(sfu, roomID, sdp) })
	sig.on("stm_bob", func(sdp string) { bob.answerRenegotiation(sfu, roomID, sdp) })

	alice.speak()
	if err := alice.offer(sfu, roomID); err != nil {
		t.Fatal(err)
	}
	bob.speak()
	if err := bob.offer(sfu, roomID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for bob.packetsFrom("stm_alice") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("bob never heard alice, so this test cannot show that muting stops it")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := sfu.SetState(context.Background(), roomID, "stm_alice", forgemedia.StateMuted); err != nil {
		t.Fatal(err)
	}
	// Alice's client keeps sending throughout — that is the point. Nothing about
	// her browser changed; the server is what stopped forwarding.
	time.Sleep(500 * time.Millisecond) // let anything in flight land
	settled := bob.packetsFrom("stm_alice")
	time.Sleep(time.Second)
	after := bob.packetsFrom("stm_alice")

	if after != settled {
		t.Errorf("bob received %d more packet(s) from a muted alice; the mute is cosmetic",
			after-settled)
	}

	// And unmuting brings her back, or mute would be a one-way door.
	if err := sfu.SetState(context.Background(), roomID, "stm_alice", forgemedia.StateActive); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for bob.packetsFrom("stm_alice") == after {
		if time.Now().After(deadline) {
			t.Fatal("alice was never heard again after unmuting")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Pausing is a mute that also stops the transcript.
func TestPausingStopsBeingWrittenDown(t *testing.T) {
	cap := newCaptureSink()
	sfu, err := forgemedia.New(forgemedia.Options{
		Config: transcribingConfig(), Log: logx.Discard(), Clock: clock.System{},
		Transcriber: cap, Turns: cap, Activity: cap,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sfu.Close()
	sig := newSignalRecorder()
	sfu.SetSignaller(sig)
	const roomID = "rom_pause"

	speaker := speakFixture(t, sfu, sig, roomID,
		realOpusFrames(t, "../llm/testdata/engineering-utterance.ogg"))
	_ = speaker

	select {
	case <-cap.done:
	case <-time.After(60 * time.Second):
		t.Fatal("nothing was transcribed before the pause, so pausing proves nothing")
	}

	if err := sfu.SetState(context.Background(), roomID, "stm_speaker", forgemedia.StatePaused); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // longer than the test silence gap
	_, before, _ := cap.captured()
	time.Sleep(2 * time.Second)
	_, after, _ := cap.captured()

	if len(after) != len(before) {
		t.Errorf("%d turn(s) were recorded while paused", len(after)-len(before))
	}
}

// An unrecognised state is refused rather than quietly treated as active.
//
// Silently defaulting would mean a client typo turned somebody's mute into a
// live microphone, which is the worst possible direction for this to fail.
func TestAnUnknownParticipantStateIsRefused(t *testing.T) {
	sfu, err := forgemedia.New(forgemedia.Options{
		Config: testConfig(5), Log: logx.Discard(), Clock: clock.System{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sfu.Close()
	sfu.SetSignaller(newSignalRecorder())
	const roomID = "rom_state"

	alice := newClient(t, "stm_alice", "usr_alice")
	alice.speak()
	if err := alice.offer(sfu, roomID); err != nil {
		t.Fatal(err)
	}

	err = sfu.SetState(context.Background(), roomID, "stm_alice", forgemedia.State("off-ish"))
	if err == nil {
		t.Fatal("an unrecognised state was accepted")
	}
	if !errs.Is(err, errs.CodeValidationFailed) {
		t.Fatalf("refused with %s, want VALIDATION_FAILED", errs.CodeOf(err))
	}
}

// A room that is off the record produces no transcript and sends no audio away.
//
// The second half is the one that matters for SEC-06: "not transcribed" has to
// mean the audio never left this machine, not that it was sent to a provider and
// the answer was discarded.
func TestARoomOffTheRecordSendsNothingToTheProvider(t *testing.T) {
	cap := newCaptureSink()
	sfu, err := forgemedia.New(forgemedia.Options{
		Config: transcribingConfig(), Log: logx.Discard(), Clock: clock.System{},
		Transcriber: cap, Turns: cap, Activity: cap,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sfu.Close()
	sig := newSignalRecorder()
	sfu.SetSignaller(sig)
	const roomID = "rom_offrecord"

	// Joined with Transcribe false — the room is off the record.
	c := newClient(t, "stm_speaker", "usr_speaker")
	c.transcribe = false
	sig.on("stm_speaker", func(sdp string) { c.answerRenegotiation(sfu, roomID, sdp) })
	speakFixtureOn(t, c, sfu, roomID, realOpusFrames(t, "../llm/testdata/engineering-utterance.ogg"))

	// Long enough that a transcribing room would have produced several segments.
	time.Sleep(8 * time.Second)

	audio, turns, speaking := cap.captured()
	if len(audio) != 0 {
		t.Errorf("%d segment(s) were sent to the speech provider from a room that is off the record", len(audio))
	}
	if len(turns) != 0 {
		t.Errorf("%d turn(s) were written down in a room that is off the record", len(turns))
	}
	// No speech-activity either: a room off the record does not open segments at
	// all, so there is nothing captured that a later change of setting could
	// flush out.
	//
	// The absent-room case is the same code path: `transcription.transcribing`
	// reads a map and an unknown room reads false, so a room nobody announced is
	// off the record too. That is not given its own test because there is no way
	// to make audio flow for a room the media plane has never seen — Join always
	// registers one — and a test that proved it by not sending any audio would
	// pass against any implementation at all.
	if len(speaking) != 0 {
		t.Errorf("%d speech-activity event(s) were produced with no segments open", len(speaking))
	}
}

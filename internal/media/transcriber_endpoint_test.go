package media_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	forgemedia "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/media"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Room speech goes to the transcriber's own endpoint, with its own key.
//
// # Why this is not a fake transcriber
//
// Every other test here substitutes the transcriber, which is right for
// segmentation and wrong for this: the question is which HOST the media plane's
// real client sends a room's speech to, and a fake has no host. So the real
// client is handed to a real SFU, pointed at two httptest servers — the chat
// endpoint and the transcriber endpoint — and real speech is spoken into a room.
//
// The workbench and the room share one client (internal/httpapi/rooms.go), so
// production's token-plan endpoint serving no speech model broke both; moving
// only one of them would leave rooms silent. See internal/llm/transcribe.go.
func TestRoomSpeech_GoesToTheTranscriberEndpointWithItsKeyAndNeverToTheChatHost(t *testing.T) {
	var mu sync.Mutex
	var chatHits int
	var sttAuths []string

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		chatHits++
		mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	defer chat.Close()
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sttAuths = append(sttAuths, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"heard at the transcriber endpoint"}}]}`))
	}))
	defer stt.Close()

	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL: chat.URL, APIKey: "sk-chat-key", Transcriber: "qwen3-asr-flash",
		TranscriberBaseURL: stt.URL, TranscriberAPIKey: "sk-asr-key",
		RequestTimeout: 10 * time.Second,
	}, logx.Discard(), clock.System{})

	sink := newCaptureSink()
	sfu, err := forgemedia.New(forgemedia.Options{
		Config: transcribingConfig(), Log: logx.Discard(), Clock: clock.System{},
		Transcriber: client, Turns: sink, Activity: sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sfu.Close()
	sig := newSignalRecorder()
	sfu.SetSignaller(sig)

	// Two seconds of real speech is one segment, and all this needs.
	frames := realOpusFrames(t, "../llm/testdata/engineering-utterance.ogg")
	speakFixture(t, sfu, sig, "rom_endpoint", frames[:100])

	select {
	case <-sink.done:
	// Two seconds of audio and a 300 ms gap; thirty is ample, and short enough
	// that the drill which breaks this does not wait a minute to see it red.
	case <-time.After(30 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("no turn was recorded: the transcriber endpoint saw %d request(s), the chat host %d",
			len(sttAuths), chatHits)
	}

	_, turns, _ := sink.captured()
	if turns[0].text != "heard at the transcriber endpoint" {
		t.Errorf("the room's turn reads %q; it did not come from the transcriber endpoint", turns[0].text)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range sttAuths {
		if a != "Bearer sk-asr-key" {
			t.Errorf("the transcriber endpoint was sent %q, want its own key", a)
		}
	}
	if chatHits != 0 {
		t.Errorf("the chat host was sent %d request(s) carrying a room's speech", chatHits)
	}
}

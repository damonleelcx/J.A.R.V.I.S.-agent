// Spike: the SFU core in miniature.
// Sender --Opus--> SFU (forwards RTP) --> Receiver.
// Proves: negotiation, per-participant track identity, and RTP forwarding.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func must(err error) {
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
}

// connect wires two peer connections together directly (local signalling).
func connect(a, b *webrtc.PeerConnection) {
	a.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			must(b.AddICECandidate(c.ToJSON()))
		}
	})
	b.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			must(a.AddICECandidate(c.ToJSON()))
		}
	})
	offer, err := a.CreateOffer(nil)
	must(err)
	must(a.SetLocalDescription(offer))
	must(b.SetRemoteDescription(offer))
	answer, err := b.CreateAnswer(nil)
	must(err)
	must(b.SetLocalDescription(answer))
	must(a.SetRemoteDescription(answer))
}

func main() {
	api := webrtc.NewAPI()

	sender, err := api.NewPeerConnection(webrtc.Configuration{})
	must(err)
	sfuIn, err := api.NewPeerConnection(webrtc.Configuration{})
	must(err)
	sfuOut, err := api.NewPeerConnection(webrtc.Configuration{})
	must(err)
	receiver, err := api.NewPeerConnection(webrtc.Configuration{})
	must(err)

	// The track the SFU will forward on. One per participant — this is what
	// makes speaker separation structural rather than inferred.
	relay, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio", "participant-alice")
	must(err)
	_, err = sfuOut.AddTrack(relay)
	must(err)

	got := make(chan string, 1)
	rtpSeen := make(chan bool, 1)

	sfuIn.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		select {
		case got <- tr.StreamID():
		default:
		}
		for {
			pkt, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			select {
			case rtpSeen <- true:
			default:
			}
			_ = relay.WriteRTP(&rtp.Packet{Header: pkt.Header, Payload: pkt.Payload})
		}
	})

	recvOK := make(chan int, 1)
	receiver.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		n := 0
		for {
			_, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			n++
			if n == 5 {
				select {
				case recvOK <- n:
				default:
				}
				return
			}
		}
	})

	out, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio", "participant-alice")
	must(err)
	_, err = sender.AddTrack(out)
	must(err)

	connect(sender, sfuIn)
	connect(sfuOut, receiver)

	go func() {
		for i := 0; i < 200; i++ {
			_ = out.WriteSample(media.Sample{
				Data:     []byte{0xf8, 0xff, 0xfe, 0x01, 0x02, 0x03},
				Duration: 20 * time.Millisecond,
			})
			time.Sleep(20 * time.Millisecond)
		}
	}()

	deadline := time.After(25 * time.Second)
	var streamID string
	for {
		select {
		case s := <-got:
			streamID = s
			fmt.Println("PASS  inbound track identified, stream id =", s)
			got = nil
		case <-rtpSeen:
			fmt.Println("PASS  RTP arriving at SFU")
			rtpSeen = nil
		case n := <-recvOK:
			fmt.Printf("PASS  receiver got %d forwarded RTP packets\n", n)
			fmt.Println("RESULT SFU-CORE-OK streamID=" + streamID)
			return
		case <-deadline:
			fmt.Println("RESULT TIMEOUT (streamID=" + streamID + ")")
			os.Exit(1)
		}
	}
}

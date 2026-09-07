// Package tts renders FORGE's own speech through a speech vendor.
//
// # Why this exists beside the model client
//
// FORGE could already speak: internal/llm/speak.go reaches the Qwen omni model
// through /chat/completions and gets PCM back. That path is not going away. This
// package exists because the VOICE is a product decision and the model is not —
// the timbre a person hears has to be the same one they hear from the other
// products in this estate, and that voice is published by a different vendor
// than the one answering the questions.
//
// So the seam is media.Speaker, which both implementations satisfy. Which one a
// deployment uses is configuration, and neither knows about the other.
//
// # Ported, not invented
//
// The Fish provider is adapted from Opportunity-Bridge-Agent's internal/tts —
// same owner, same vendor, same voice — including its backbone allowlist. The
// adaptation is the transport: that product hands MP3 to a browser <audio>
// element, and this one streams 16-bit PCM into a media plane.
package tts

import "context"

// Speaker renders text as speech, streaming PCM as it arrives.
//
// This is media.Speaker's shape, deliberately: onPCM receives 16-bit
// little-endian mono samples at llm.SpeechSampleRate, and returning an error
// from it stops the stream, which is how an interruption cancels an utterance
// the vendor is still generating.
//
// Streaming rather than returning bytes is not a refinement. A whole answer
// synthesised before the first sound would put several seconds between somebody
// finishing a sentence and FORGE starting one, which is the difference between
// a conversation and a transaction.
type Speaker interface {
	Name() string
	Speak(ctx context.Context, text string, onPCM func([]byte) error) error
}

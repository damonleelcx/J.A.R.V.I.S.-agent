package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Drawing a reference picture.
//
// # Why this is not Complete with another role
//
// It runs against the SAME endpoint, the same key and the same /chat/completions
// path as every other call here — and the reply is a different wire format. Three
// shapes were tried against the live endpoint on 2026-09-09 and only one works:
//
//	POST /images/generations              → 400 "url error, please check url"
//	POST /chat/completions, content ""    → 400 "Input should be a valid list:
//	                                             input.messages.0.content"
//	POST /chat/completions, content [ … ] → 200
//
// and the 200 does not carry `choices` at the top level, which is the only thing
// Complete knows how to read:
//
//	{"output":{"choices":[{"message":{"content":[{"type":"image","image":"https://…"}]}}]},
//	 "usage":{"image_count":1,"size":"2048*2048"}}
//
// So routing this through Complete would return "the model returned nothing" on
// a perfectly successful generation. It is its own path, deliberately, and the
// two decoders are never asked to handle each other's format.
//
// Asynchronous submission is refused on this deployment's key —
// `403 AccessDenied: current user api does not support asynchronous calls` — so
// the call is synchronous and holds the connection for the whole generation.
// Measured at roughly 30–60 seconds.
//
// # Why it returns a URL rather than the image
//
// The provider hands back an OSS URL, and the vision model reads that URL
// directly — proven live. So nothing here downloads 5.9 MB, stores it, or needs
// egress to the object store: the URL travels to RoleVision exactly as an
// uploaded data URI already does. What is generated is never persisted, which is
// also why it cannot go stale in the record.

// Illustrator draws a reference picture from a description.
//
// Separate from Client because a deployment without an image model has no
// illustrator at all, and the difference must be a nil rather than a call that
// fails: "this deployment does not draw" and "drawing failed" are different
// facts, the same distinction RoleVision already makes.
type Illustrator interface {
	// Draw returns the URL of a generated image.
	Draw(ctx context.Context, prompt string) (string, error)
	// IllustratorModel is which model backs it, empty when none is configured.
	IllustratorModel() string
}

// illustrateTimeout is the wall clock one generation gets.
//
// Longer than a chat call on purpose: measured at 30–60s against wan2.7-image
// for a 2048×2048 sheet, where the client's ordinary RequestTimeout is tuned for
// a conversation. Bounded anyway, because the turn budget above it is not a
// reason for one call to hang forever.
const illustrateTimeout = 4 * time.Minute

// IllustratorModel reports the configured image model, or "" for none.
func (c *OpenAICompatible) IllustratorModel() string { return c.models[RoleIllustrator] }

// Draw generates one image and returns its URL.
func (c *OpenAICompatible) Draw(ctx context.Context, prompt string) (string, error) {
	const op = "llm.OpenAICompatible.Draw"

	model := c.models[RoleIllustrator]
	if model == "" {
		return "", errs.New(op, errs.CodeConfigInvalid).
			WithDetail("no image model is configured, so this deployment does not draw a " +
				"reference. Set FORGE_LLM_IMAGE_MODEL.")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", errs.New(op, errs.CodeInvariantViolated).
			WithDetail("a drawing request must carry a description")
	}

	// content is a LIST even though it holds one text part. A plain string is
	// refused by this endpoint for this model family — see the note above.
	body, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": prompt}},
		}},
	})
	if err != nil {
		return "", errs.Wrap(op, errs.CodeSerializationFail, err)
	}

	ctx, cancel := context.WithTimeout(ctx, illustrateTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", errs.Wrap(op, errs.CodeInternal, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	started := c.clock.Now()
	// Its own client, because the shared one carries the CHAT RequestTimeout and
	// a generation outlives it. Reusing it would fail every drawing on a
	// deployment tuned for a fast conversation, and fail it as a timeout — which
	// points at the provider rather than at the setting.
	resp, err := (&http.Client{Timeout: illustrateTimeout}).Do(req)
	if err != nil {
		return "", errs.Wrap(op, errs.CodeExternalUnavailable, err).
			WithDetail("the image model could not be reached")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", errs.Wrap(op, errs.CodeExternalProtocol, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", errs.New(op, errs.CodeExternalUnavailable).
			WithDetail("the image model refused: %d %s", resp.StatusCode, truncate(string(raw), 300))
	}

	url, count, err := decodeDrawing(raw)
	if err != nil {
		return "", errs.Wrap(op, errs.CodeExternalProtocol, err).
			WithDetail("the image model replied in a shape this build cannot read: %s",
				truncate(string(raw), 300))
	}
	c.log.Info(ctx, logx.EventLLMCompleted, "role", string(RoleIllustrator), "model", model,
		"images", count, "latency_ms", c.clock.Now().Sub(started).Milliseconds())
	return url, nil
}

// decodeDrawing reads the provider's own reply shape.
//
// Split out so the format is fenced without spending money: a live call is the
// only way to learn this shape and the worst possible way to check it has not
// changed underneath us.
func decodeDrawing(raw []byte) (url string, images int, err error) {
	var reply struct {
		Output struct {
			Choices []struct {
				Message struct {
					Content []struct {
						Type  string `json:"type"`
						Image string `json:"image"`
					} `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		} `json:"output"`
		Usage struct {
			ImageCount int `json:"image_count"`
		} `json:"usage"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return "", 0, err
	}
	// A 200 carrying an error code. Named rather than read as "no image",
	// because the two want different answers from the caller.
	if reply.Code != "" {
		return "", 0, fmt.Errorf("%s: %s", reply.Code, reply.Message)
	}
	for _, ch := range reply.Output.Choices {
		for _, part := range ch.Message.Content {
			if part.Type == "image" && strings.HasPrefix(part.Image, "https://") {
				return part.Image, reply.Usage.ImageCount, nil
			}
		}
	}
	return "", 0, fmt.Errorf("the reply carried no image")
}

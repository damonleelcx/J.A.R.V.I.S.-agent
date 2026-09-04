package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// The wire shape of a message with and without images (PRD VIS-01).

// A text-only message keeps the exact shape it had before images existed.
//
// This is the fence that matters most here. The provider accepts either a
// string or an array for `content`, but a change of shape on EVERY request to
// support a feature used on almost none of them is the kind of thing that works
// against one provider and quietly fails against another — and it would fail on
// the paths nobody was testing.
func TestATextOnlyMessageIsUnchangedOnTheWire(t *testing.T) {
	raw, err := json.Marshal(Message{Role: User, Content: "make it taller"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, isString := got["content"].(string); !isString {
		t.Fatalf("content is %T, not a string: %s\n"+
			"Every existing request must keep the shape it shipped with", got["content"], raw)
	}
	if _, has := got["images"]; has {
		t.Errorf("the images field reached the provider: %s", raw)
	}
}

// With images, content becomes the provider's multi-part array.
func TestImagesBecomeContentParts(t *testing.T) {
	raw, err := json.Marshal(Message{
		Role: User, Content: "model this", Images: []string{"data:image/png;base64,AAAA"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("content did not become an array: %v\n%s", err, raw)
	}
	if len(got.Content) != 2 {
		t.Fatalf("expected a text part and an image part, got %d: %s", len(got.Content), raw)
	}
	if got.Content[0].Type != "text" || got.Content[0].Text != "model this" {
		t.Errorf("the text part is wrong: %s", raw)
	}
	if got.Content[1].Type != "image_url" || got.Content[1].ImageURL == nil ||
		!strings.HasPrefix(got.Content[1].ImageURL.URL, "data:image/png") {
		t.Errorf("the image part is wrong: %s", raw)
	}

	// A picture with no words is a legal turn — somebody drops in a sketch and
	// says nothing — and it must not emit an empty text part, which some
	// providers reject.
	raw, _ = json.Marshal(Message{Role: User, Images: []string{"data:image/png;base64,AAAA"}})
	if strings.Contains(string(raw), `"type":"text"`) {
		t.Errorf("an empty text part was sent alongside the image: %s", raw)
	}
}

// The vision role must be able to make a request at all.
//
// It was added to the model map and left out of Valid(), and both Complete and
// Stream gate on Valid() — so every image turn was refused as an unknown role,
// after the picture had been read, encoded and posted. Found in a live run.
func TestTheVisionRoleIsAChatRole(t *testing.T) {
	if !RoleVision.Valid() {
		t.Fatal("RoleVision is not valid, so Complete and Stream both refuse it before " +
			"looking at the model map")
	}
	// And the roles that are not chat roles stay out: they have their own
	// methods and would resolve to an empty model here.
	for _, r := range []Role{RoleTranscriber, RoleSpeaker} {
		if r.Valid() {
			t.Errorf("%q is not a chat role and reports itself as one", r)
		}
	}
}

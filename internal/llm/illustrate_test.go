package llm

import "testing"

// The provider's own reply shape is read, and it is not OpenAI's.
//
// # Why this is fenced without a live call
//
// A generation costs money and takes 30-60 seconds, so the shape cannot be
// checked on every run — and a live call is the worst possible way to learn that
// the format changed, because it fails in production with a bill attached. This
// is the recorded shape from the live spike on 2026-09-09, verbatim.
func TestDecodeDrawing(t *testing.T) {
	t.Run("the shape the provider actually returns", func(t *testing.T) {
		raw := []byte(`{"request_id":"1bbea636",
		 "output":{"choices":[{"message":{"role":"assistant","content":[
		   {"type":"image","image":"https://dashscope-7c2c.oss-accelerate.aliyuncs.com/1d/cc/x.png"}]},
		   "finish_reason":"stop"}],"finished":true},
		 "usage":{"image_count":1,"input_tokens":978,"output_tokens":2,"size":"2048*2048"}}`)
		url, n, err := decodeDrawing(raw)
		if err != nil {
			t.Fatalf("the recorded live reply did not decode: %v", err)
		}
		if url != "https://dashscope-7c2c.oss-accelerate.aliyuncs.com/1d/cc/x.png" {
			t.Errorf("wrong url: %q", url)
		}
		if n != 1 {
			t.Errorf("image_count = %d, want 1", n)
		}
	})

	t.Run("an OpenAI-shaped reply is NOT silently accepted", func(t *testing.T) {
		// The whole reason this has its own decoder. If this ever passes, the
		// two formats have been conflated and one of them is being misread.
		raw := []byte(`{"choices":[{"message":{"role":"assistant","content":"here is your image"}}]}`)
		if _, _, err := decodeDrawing(raw); err == nil {
			t.Error("a chat-shaped reply decoded as a drawing, so a text answer would be " +
				"passed on as an image URL")
		}
	})

	t.Run("a 200 carrying an error code is named, not read as no image", func(t *testing.T) {
		raw := []byte(`{"code":"InvalidParameter","message":"url error, please check url"}`)
		_, _, err := decodeDrawing(raw)
		if err == nil {
			t.Fatal("an error reply decoded as success")
		}
		if got := err.Error(); got != "InvalidParameter: url error, please check url" {
			t.Errorf("the provider's own words were lost: %q", got)
		}
	})

	t.Run("a non-https image is refused", func(t *testing.T) {
		// The URL is handed straight to the vision model. A file:// or data:
		// value arriving here would be passed on unexamined.
		raw := []byte(`{"output":{"choices":[{"message":{"content":[
		  {"type":"image","image":"file:///etc/passwd"}]}}]}}`)
		if _, _, err := decodeDrawing(raw); err == nil {
			t.Error("a non-https image URL was accepted and would be sent onward as-is")
		}
	})
}

// The illustrator is not a chat role.
//
// Complete and Stream gate on Role.Valid(), and a drawing routed through them
// would be decoded by a parser that cannot read its answer — reporting "the
// model returned nothing" on a perfectly successful generation.
func TestIllustratorIsNotAChatRole(t *testing.T) {
	if RoleIllustrator.Valid() {
		t.Error("RoleIllustrator is accepted by Complete/Stream, whose decoder cannot read " +
			"the reply shape it produces")
	}
	for _, r := range AllRoles() {
		if r == RoleIllustrator {
			t.Error("RoleIllustrator is in AllRoles, which reads as roles a deployment must " +
				"configure — drawing is optional and absent by default")
		}
	}
}

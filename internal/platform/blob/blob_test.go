package blob

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The key is the SHA-256 of the bytes, in a form that cannot name two objects for
// the same content.
func TestKeyOf_IsTheSHA256OfTheBytes(t *testing.T) {
	k, n, err := KeyOf(strings.NewReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	// The published SHA-256 test vector for "abc".
	const want = "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if k != want || n != 3 {
		t.Fatalf("KeyOf(abc) = %s (%d bytes), want %s (3)", k, n, want)
	}
	if err := k.Validate(); err != nil {
		t.Errorf("a key KeyOf produced does not validate: %v", err)
	}
}

func TestKey_RefusesAnythingButCanonicalHex(t *testing.T) {
	good := "sha256:" + strings.Repeat("a", 64)
	for _, bad := range []string{
		"",
		strings.Repeat("a", 64),                // no algorithm
		"md5:" + strings.Repeat("a", 32),       // wrong algorithm
		"sha256:" + strings.Repeat("a", 63),    // short
		"sha256:" + strings.Repeat("g", 64),    // not hex
		"sha256:" + strings.Repeat("A", 64),    // uppercase names a second object for the same bytes
		"sha256:../" + strings.Repeat("a", 61), // a path, not a digest
	} {
		if err := Key(bad).Validate(); err == nil {
			t.Errorf("%q was accepted as a blob key", bad)
		}
	}
	if err := Key(good).Validate(); err != nil {
		t.Errorf("a canonical key was refused: %v", err)
	}
	if got := objectName(Key(good)); got != "blobs/sha256/aa/"+strings.Repeat("a", 64) {
		t.Errorf("object name %q is not under blobs/, the only prefix the IAM policy grants", got)
	}
}

// A deployment with no bucket refuses every call, and names the setting.
func TestDisabled_RefusesLoudlyAndNamesTheSetting(t *testing.T) {
	s := Disabled()
	if s.Available() {
		t.Fatal("a store with no bucket says it is available")
	}
	ctx := context.Background()
	_, putErr := s.Put(ctx, bytes.NewReader([]byte("x")))
	_, getErr := s.Get(ctx, Key("sha256:"+strings.Repeat("a", 64)))
	_, hasErr := s.Has(ctx, Key("sha256:"+strings.Repeat("a", 64)))
	for name, err := range map[string]error{"Put": putErr, "Get": getErr, "Has": hasErr} {
		if errs.CodeOf(err) != errs.CodeConnectorUnavailable {
			t.Errorf("%s: code %s, want %s", name, errs.CodeOf(err), errs.CodeConnectorUnavailable)
		}
		if !strings.Contains(errs.DetailOf(err), "FORGE_BLOB_BUCKET") {
			t.Errorf("%s does not name the setting that fixes it: %s", name, errs.DetailOf(err))
		}
	}
}

// A blob whose bytes do not hash to their key is refused at the end of the read,
// not handed over as good.
func TestVerifying_RefusesBytesThatDoNotMatchTheirKey(t *testing.T) {
	good, _, _ := KeyOf(strings.NewReader("the right bytes"))

	var reported string
	r := newVerifying(io.NopCloser(strings.NewReader("the wrong bytes")), good, func(got string) { reported = got })
	_, err := io.ReadAll(r)
	if errs.CodeOf(err) != errs.CodeStateCorrupt {
		t.Fatalf("reading corrupt bytes ended with %v, want %s", err, errs.CodeStateCorrupt)
	}
	if reported == "" {
		t.Error("the mismatch was not reported to the caller's hook, so nothing is logged")
	}

	ok := newVerifying(io.NopCloser(strings.NewReader("the right bytes")), good, nil)
	if b, err := io.ReadAll(ok); err != nil || string(b) != "the right bytes" {
		t.Errorf("matching bytes were refused: %q, %v", b, err)
	}
}

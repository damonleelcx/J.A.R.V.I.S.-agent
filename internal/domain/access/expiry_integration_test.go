package access_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Access that runs out (PRD AGT-03, "time-bound"; issue 20).
//
// # What these hold
//
// Four of AGT-03's five clauses were built in 0010 and are fenced elsewhere in
// this file's neighbours. The fifth was not built at all: there was no expiry
// column, so a membership lasted until somebody remembered to revoke it, and
// the qualified-review authority that RAISES a project's risk ceiling had the
// same problem with sharper consequences.
//
// These fence the measurement points rather than a wall-clock duration: that an
// unstated expiry becomes the default, that an expired grant refuses everywhere
// access is decided, and that the owner exemption is a decision rather than a
// hole. The fake clock is moved, not slept through.

// speaks returns a service that writes its audit events somewhere readable.
//
// The harness's own service discards them, which is right for the tests that do
// not care. An expiry is the one way access ends with nobody deciding it, so the
// event is the only record that it happened — and a fence that did not read it
// would let the line be deleted.
func speaks(t *testing.T, h *harness) (*access.Service, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return access.NewService(h.pool, h.clk, logx.New(logx.Options{Output: &buf})), &buf
}

func expiryOf(t *testing.T, h *harness, userID string) *time.Time {
	t.Helper()
	var at *time.Time
	if err := h.pool.QueryRow(context.Background(),
		`select expires_at from forge_project_members where project_id = $1 and user_id = $2`,
		h.project, userID).Scan(&at); err != nil {
		t.Fatalf("reading the stored expiry: %v", err)
	}
	return at
}

// A grant nobody dated still has an end.
//
// This is the clause that decides whether AGT-03 survives contact with the
// ordinary path. Every grant made from a form with no date field, every
// `forgectl access grant` typed in a hurry, every test fixture: if an unstated
// expiry meant "forever" the requirement would hold only for the callers who
// remembered it, which is the state issue 20 found.
func TestAccessExpiry_AGrantWithNoExpiryGetsTheDefault(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other, Role: access.RoleContributor, By: h.owner}); err != nil {
		t.Fatal(err)
	}
	at := expiryOf(t, h, h.other)
	if at == nil {
		t.Fatal("a grant with no stated expiry was stored with none at all.\n" +
			"PRD AGT-03 asks for time-bound access; access that lasts until somebody " +
			"remembers to revoke it is not time-bound, it is permanent with a chore attached")
	}
	want := h.clk.Now().Add(access.DefaultGrantLifetime)
	if d := at.Sub(want); d > time.Second || d < -time.Second {
		t.Errorf("the default grant ends at %s, want %s (DefaultGrantLifetime from the grant)",
			at.UTC().Format(time.RFC3339), want.UTC().Format(time.RFC3339))
	}
}

// An explicit end is honoured exactly, and is not quietly widened to the default.
func TestAccessExpiry_AnExplicitUntilIsHonoured(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	until := h.clk.Now().Add(72 * time.Hour)
	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other, Role: access.RoleMaintainer,
		By: h.owner, Until: until}); err != nil {
		t.Fatal(err)
	}
	at := expiryOf(t, h, h.other)
	if at == nil || !at.Equal(until) {
		t.Fatalf("a grant made until %s expires at %v; an expiry somebody typed must not be "+
			"replaced by the default", until.UTC().Format(time.RFC3339), at)
	}
}

// The fence the whole clause rests on: past its end, a grant is not access.
//
// Checked at every place access is decided, not only at Require. A listing that
// still offered the project, or a strand check that still counted the holder as
// an owner, would be a second answer to "may this person do this here" — which
// is the failure the whole package was built to end.
func TestAccessExpiry_AnExpiredGrantIsNotAccess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	svc, log := speaks(t, h)

	until := h.clk.Now().Add(24 * time.Hour)
	if err := svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other, Role: access.RoleContributor,
		By: h.owner, Until: until}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Require(ctx, h.project, h.other, access.PermProjectRead); err != nil {
		t.Fatalf("the grant does not work before it expires: %v", err)
	}

	h.clk.Advance(25 * time.Hour)

	err := svc.Require(ctx, h.project, h.other, access.PermProjectRead)
	if err == nil {
		t.Fatal("an expired grant still reads the project.\n" +
			"PRD AGT-03's time-bound clause is the whole of this: an expiry that does not " +
			"refuse is a date stored beside access that never ends")
	}
	if !errs.Is(err, errs.CodeNotFound) {
		t.Errorf("an expired member was refused with %s; a project they can no longer see must "+
			"read as one that does not exist, exactly as for somebody who was never in it",
			errs.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("the refusal does not say the access ran out: %v", err)
	}

	// The listing agrees with the check. A project in "what may I open" that
	// refuses when opened is two answers to one question.
	visible, err := svc.Projects(ctx, h.other)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := visible[h.project]; ok {
		t.Error("a project whose membership expired is still listed as one the caller is in")
	}

	// But the membership row is still THERE, and marked. Expiry is not
	// revocation: who had access, and until when, is the first question asked
	// after anything goes wrong.
	members, err := svc.Members(ctx, h.project)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range members {
		if m.UserID != h.other {
			continue
		}
		found = true
		if !m.Expired {
			t.Error("the lapsed membership is listed as current")
		}
		if m.ExpiresAt == "" {
			t.Error("the lapsed membership does not say when it ran out")
		}
	}
	if !found {
		t.Error("the lapsed membership vanished from the members list.\n" +
			"Dropping it reads as 'they were removed', which nobody did, and it destroys the " +
			"answer to 'who had access and until when' at the moment somebody starts asking")
	}

	if !strings.Contains(log.String(), string(logx.EventAccessExpired)) {
		t.Errorf("no %s audit event was written when access ran out.\n"+
			"An expiry is the one way access ends with no actor; without the event there is "+
			"nothing at all recording that it happened.\nlog: %s",
			logx.EventAccessExpired, log.String())
	}
}

// An owner does not lapse unless somebody says so.
//
// Not an oversight in the clause. An owner administers the project, and one that
// expired on a timer would leave a project nobody can add a member to, change a
// role in, or restore access to — including the person who lost it. That is the
// state wouldStrandProject refuses to create by every other route, and a default
// that produced it on a delay would be the same hazard with patience.
func TestAccessExpiry_AnOwnerDoesNotLapseByDefault(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// The creating owner, written by EnsureOwner.
	if at := expiryOf(t, h, h.owner); at != nil {
		t.Errorf("the project's creating owner expires at %s. A project whose last owner lapses "+
			"cannot be administered again through the product at all", at.UTC().Format(time.RFC3339))
	}
	// And a granted one.
	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other, Role: access.RoleOwner, By: h.owner}); err != nil {
		t.Fatal(err)
	}
	if at := expiryOf(t, h, h.other); at != nil {
		t.Errorf("an owner granted with no stated expiry lapses at %s", at.UTC().Format(time.RFC3339))
	}

	// An explicit end is still allowed — a temporary owner for a handover is a
	// real thing, and it is a decision somebody typed.
	until := h.clk.Now().Add(48 * time.Hour)
	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.third, Role: access.RoleOwner,
		By: h.owner, Until: until}); err != nil {
		t.Fatal(err)
	}
	if at := expiryOf(t, h, h.third); at == nil || !at.Equal(until) {
		t.Errorf("an owner granted until %s expires at %v", until.UTC().Format(time.RFC3339), at)
	}
}

// "Forever" is a word only an owner may use.
//
// Without this, Forever would be the escape hatch that empties the clause: every
// caller who found expiry inconvenient would set it, and AGT-03 would hold for
// nobody. Owners are exempt because they are already exempt by default.
func TestAccessExpiry_OnlyAnOwnerMayBeGrantedForever(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other, Role: access.RoleMaintainer,
		By: h.owner, Forever: true})
	if err == nil {
		t.Fatal("a maintainer was granted access with no expiry, which is the clause with a " +
			"hole in it: anyone who found AGT-03 inconvenient would grant this way")
	}
	if !errs.Is(err, errs.CodeValidationFailed) {
		t.Errorf("refused with %s", errs.CodeOf(err))
	}
	if err := h.svc.SetRole(ctx, access.Grant{
		ProjectID: h.project, UserID: h.other, Role: access.RoleOwner,
		By: h.owner, Forever: true}); err != nil {
		t.Fatalf("an owner cannot be granted without an expiry, which is the default anyway: %v", err)
	}
}

// The migration's literal and the Go constant say the same number.
//
// 0027 has to write the default lifetime as `interval '90 days'` because a
// migration cannot read Go, and its backfill dates every authority recorded
// before it from that interval. Two copies of one number drift, and the drift
// here would silently give every pre-existing raised ceiling a different life
// from every new one.
//
// Not an integration test: it reads a file.
func TestMigrationAndCodeAgreeOnTheDefaultGrantLifetime(t *testing.T) {
	path := filepath.Join("..", "..", "platform", "db", "sql", "0027_access_expiry.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`interval '(\d+) days'`).FindSubmatch(body)
	if m == nil {
		t.Fatalf("%s no longer writes its backfill as an interval in days; this fence reads that "+
			"literal to hold it against access.DefaultGrantLifetime", path)
	}
	days, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	if want := int(access.DefaultGrantLifetime.Hours() / 24); days != want {
		t.Errorf("%s backfills with %d days and access.DefaultGrantLifetime is %d days.\n"+
			"One of them moved. An authority recorded before the migration would then live a "+
			"different length of time from one recorded after it, for no reason anybody chose",
			path, days, want)
	}
}

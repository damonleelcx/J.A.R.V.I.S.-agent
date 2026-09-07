package db_test

import (
	"context"
	"net"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// WaitForConnect exists so forge-worker stops dying when it starts a second
// before postgres does. The whole value of it is in the line it draws, so that
// is what these test: it must wait when nothing answered, and it must NOT wait
// when postgres answered and said no.
//
// The second half is the one worth having. A retry loop that also waits out a
// wrong password turns a five-second misconfiguration into a process that never
// starts and never explains itself — strictly worse than the crash it replaces,
// because the crash at least printed the reason.

func waitCfg(url string) config.DBConfig {
	return config.DBConfig{
		URL: url, MaxConns: 4, MinConns: 0,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute,
		// Deliberately short. These tests assert on elapsed time, and a ten
		// second dial timeout would make "returned immediately" unmeasurable.
		ConnectTimeout: 2 * time.Second,
	}
}

// A port with nothing on it, held closed for the duration of the test by
// binding and immediately releasing it — asking the kernel for a free port is
// more reliable than picking a number and hoping.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestWaitForConnectWaitsWhenNothingIsAnswering(t *testing.T) {
	addr := closedPort(t)
	limit := 1500 * time.Millisecond

	start := time.Now()
	pool, err := db.WaitForConnect(context.Background(),
		waitCfg("postgres://forge:forge@"+addr+"/forge?sslmode=disable"),
		logx.Discard(), limit)
	elapsed := time.Since(start)

	if err == nil {
		pool.Close()
		t.Fatal("connected to a port with nothing on it")
	}
	// The point is that it waited. Returning the same error instantly would be
	// the old behaviour wearing the new function's name.
	if elapsed < limit {
		t.Errorf("gave up after %v, before the %v limit — it did not retry", elapsed, limit)
	}
	// And that it stopped. An unbounded wait is a process that hangs for ever
	// on a database that is never coming.
	if elapsed > limit+3*time.Second {
		t.Errorf("took %v, well past the %v limit — the deadline is not bounding it", elapsed, limit)
	}
	if got := errs.CodeOf(err); got != errs.CodeDatabaseUnavail {
		t.Errorf("code = %v, want %v", got, errs.CodeDatabaseUnavail)
	}
}

func TestWaitForConnectDoesNotWaitOutARefusal(t *testing.T) {
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. Run `make db-up` then `make test-integration`.")
	}
	// Same host and database, wrong password. Postgres answers and refuses
	// (28P01), which is a fact about the configuration and not about the clock.
	//
	// Parsed rather than string-spliced: a connection string may or may not
	// already carry credentials, and getting that wrong would produce an
	// unparseable URL that fails fast for the WRONG reason — which would make
	// this test pass while proving nothing.
	u, perr := neturl.Parse(url)
	if perr != nil {
		t.Fatalf("FORGE_TEST_DATABASE_URL does not parse: %v", perr)
	}
	u.User = neturl.UserPassword("forge", "definitely_not_the_password")
	bad := u.String()

	limit := 20 * time.Second
	start := time.Now()
	pool, err := db.WaitForConnect(context.Background(), waitCfg(bad), logx.Discard(), limit)
	elapsed := time.Since(start)

	if err == nil {
		pool.Close()
		t.Fatal("a deliberately wrong password connected")
	}
	// The assertion that matters. If this ever takes the full limit, the
	// discriminator has broken and every credential mistake now presents as a
	// process that hangs at boot with no explanation.
	if elapsed > 5*time.Second {
		t.Errorf("took %v to report a refused login; it must not be retried, because it will be "+
			"refused identically in ten minutes. Check worthWaitingFor still recognises *pgconn.PgError.", elapsed)
	}
}

func TestWaitForConnectDoesNotWaitOutAnUnparseableURL(t *testing.T) {
	start := time.Now()
	pool, err := db.WaitForConnect(context.Background(),
		waitCfg("this is not a connection string"),
		logx.Discard(), 20*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		pool.Close()
		t.Fatal("an unparseable connection string connected")
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v to reject an unparseable URL; there is nothing to wait for", elapsed)
	}
	if got := errs.CodeOf(err); got != errs.CodeConfigInvalid {
		t.Errorf("code = %v, want %v", got, errs.CodeConfigInvalid)
	}
}

func TestWaitForConnectReturnsAtOnceWhenTheDatabaseIsUp(t *testing.T) {
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. Run `make db-up` then `make test-integration`.")
	}
	start := time.Now()
	pool, err := db.WaitForConnect(context.Background(), waitCfg(url), logx.Discard(), 20*time.Second)
	if err != nil {
		t.Fatalf("WaitForConnect against a live database: %v", err)
	}
	defer pool.Close()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %v against a database that was already up; the happy path must not pay for the retry", elapsed)
	}
}

package db

import (
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time proof that Querier is satisfied by both the pool (autocommit)
// and a transaction.
//
// This is load-bearing, not decoration. Repositories take a Querier so that the
// same method works inside and outside a transaction — the durable engine
// claims a job, writes a checkpoint, and appends a timeline event as one atomic
// unit, and those cannot be three separate autocommit writes. If Querier ever
// drifts from pgx's real signatures, this file fails the build immediately
// rather than at the first repository that tries to use it.
//
// The first version of Querier declared Exec as returning a locally-defined
// interface instead of pgconn.CommandTag, and satisfied neither type. These
// assertions caught it before any repository was written against it.
var (
	_ Querier = (*pgxpool.Pool)(nil)
	_ Querier = (pgx.Tx)(nil)
)

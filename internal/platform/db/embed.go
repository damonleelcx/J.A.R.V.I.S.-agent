package db

import "embed"

// Files embeds the SQL migration chain into the binary.
//
// Why embedded rather than read from disk: a binary that needs a sibling
// directory to migrate is a binary that can be deployed in a state where it
// cannot start. Embedding makes "the code and its schema shipped together" a
// property of the artifact rather than of the deployment procedure.
//
//go:embed all:sql
var Files embed.FS

// MigrationsDir is the path inside Files holding the migration chain.
const MigrationsDir = "sql"

package storage

import "errors"

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("storage: not found")

// ErrSchemaTooNew is returned when the on-disk schema version is newer than the
// running binary understands. RiskKernel refuses to start in this case rather
// than risk corrupting a user's data (downgrade protection, COMPATIBILITY.md).
var ErrSchemaTooNew = errors.New("storage: on-disk schema is newer than this binary; upgrade riskkernel")

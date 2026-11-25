package fetch

import "context"

// LockDB maps [Source.Hash] to a trusted ERIS capability URN. Operations are
// best effort and should respect the request context. A failed write leaves
// the fetch result usable. A database used by concurrent fetches must support
// concurrent calls. Share a database only among callers allowed to use the
// capabilities it contains.
type LockDB interface {
	// Lookup returns the canonical URN, or an empty string on a miss or failure.
	Lookup(ctx context.Context, hash [32]byte) string

	// Add atomically stores the first binding for hash. Later calls preserve it.
	// Implementations can report conflicts and write failures through diagnostics.
	Add(ctx context.Context, hash [32]byte, urn string)
}

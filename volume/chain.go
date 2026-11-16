package volume

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/asciimoth/goeris"
)

// ErrInvalidChain means that a nonempty path list contains an empty entry.
var ErrInvalidChain = errors.New("eris-volume: invalid chain path list")

// Chain reads and writes through an ordered list of volumes. Block operations
// and OnError use the embedded eris.CombinedBlockStore. Each operation stops at
// the first success; writes do not copy blocks to the remaining volumes.
// Options updates continue after false, nil until a sink returns true, nil
// or the list ends.
// A file root rejects writes, so the combined store proceeds to the next entry.
//
// OpenChain owns the volume handles. Close the chain after all operations have
// finished. Set OnError before use and make it safe for concurrent calls.
// Do not replace the embedded CombinedBlockStore. The zero Chain is empty.
type Chain struct {
	eris.CombinedBlockStore
	volumes []*Volume
}

// OpenChain opens a semicolon-separated list of existing volume paths, in order.
// An empty string uses DefaultChainPaths. Empty entries in a nonempty list are
// invalid; they never refer to the current directory. Paths are literal: there
// is no whitespace trimming, quoting, escaping, or shell expansion. A semicolon
// cannot occur within a path. Colons, including Windows drive letters, are
// allowed. Relative paths use the current working directory.
//
// If any volume cannot be opened, OpenChain closes all handles opened so far
// and returns an error. It creates no roots and does not parse CBOR files.
// Applications may pass os.Getenv("ERIS_BLOCK_PATH"). Prefer appending new paths
// so the existing read and write order stays unchanged.
func OpenChain(pathList string) (*Chain, error) {
	var paths []string
	if pathList == "" {
		var err error
		paths, err = DefaultChainPaths()
		if err != nil {
			return nil, err
		}
	} else {
		paths = strings.Split(pathList, ";")
	}
	// Validate the whole list before opening any handles.
	for index, name := range paths {
		if name == "" {
			return nil, fmt.Errorf("volume chain entry %d: %w", index, ErrInvalidChain)
		}
	}
	chain := &Chain{}
	stores := make([]any, 0, len(paths))
	for index, name := range paths {
		v, err := Open(name)
		if err != nil {
			err = fmt.Errorf("volume chain entry %d (%q): %w", index, name, err)
			return nil, errors.Join(err, chain.Close())
		}
		chain.volumes = append(chain.volumes, v)
		stores = append(stores, v)
	}
	chain.CombinedBlockStore = *eris.NewCombinedBlockStore(stores...)
	return chain, nil
}

// Close closes every volume opened by OpenChain, even if an earlier close
// fails. It joins any close errors. Closing an empty chain succeeds.
func (chain *Chain) Close() error {
	if chain == nil {
		return fs.ErrClosed
	}
	var errs []error
	for _, v := range chain.volumes {
		if err := v.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

var (
	_ eris.BlockStore = (*Chain)(nil)
	_ io.Closer       = (*Chain)(nil)
)

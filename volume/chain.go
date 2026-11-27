package volume

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/streamio"
)

// ErrInvalidChain means that a nonempty path list contains an empty entry.
var ErrInvalidChain = errors.New("eris-volume: invalid chain path list")

// Chain reads and writes through an ordered list of volumes. Block operations
// and OnError use the embedded eris.CombinedBlockStore. Reads and writes stop at
// the first success; writes do not copy blocks to the remaining volumes.
// Prefetch hints go to all volumes, which currently ignore them.
// Options updates continue after false, nil until a sink returns true, nil
// or the list ends.
// A file root rejects writes, so the combined store proceeds to the next entry.
//
// The chain owns the volume handles. Close it after all operations have
// finished. Set OnError before use and make it safe for concurrent calls.
// Do not replace the embedded CombinedBlockStore. The zero Chain is empty.
type Chain struct {
	eris.CombinedBlockStore
	volumes []*Volume
	paths   []string
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
	if pathList == "" {
		paths, err := DefaultChainPaths()
		if err != nil {
			return nil, err
		}
		return openChain(paths, Open)
	}
	return openChain(strings.Split(pathList, ";"), Open)
}

// OpenAvailableChain uses the path list and default order of [OpenChain].
// It skips missing roots. If a selected platform default path cannot be found,
// it tries to create the root directory and any missing parents. It
// skips the entry if directory creation fails. This also applies when the
// path list names a default root explicitly.
//
// An empty chain is valid. Other setup errors are returned as in [OpenChain].
func OpenAvailableChain(pathList string) (*Chain, error) {
	defaults, err := defaultChainPaths()
	if pathList == "" && err != nil {
		return nil, err
	}
	return openAvailableChain(pathList, defaults)
}

func openAvailableChain(pathList string, defaults []string) (*Chain, error) {
	paths := defaults
	if pathList != "" {
		paths = strings.Split(pathList, ";")
	}
	return openChain(paths, func(name string) (*Volume, error) {
		var v *Volume
		info, err := os.Stat(name)
		if err == nil {
			v, err = openVolume(name, info)
		} else if path := matchingDefaultPath(name, defaults); path != "" {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return nil, nil
			}
			v, err = Open(name)
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return v, err
	})
}

func matchingDefaultPath(name string, defaults []string) string {
	absolute, err := streamio.AbsolutePath(name, "")
	if err != nil {
		return ""
	}
	for _, path := range defaults {
		if absolute == path || (filepath.Separator == '\\' && strings.EqualFold(absolute, path)) {
			return path
		}
	}
	return ""
}

func openChain(paths []string, open func(string) (*Volume, error)) (*Chain, error) {
	// Validate the whole list before opening any handles.
	for index, name := range paths {
		if name == "" {
			return nil, fmt.Errorf("volume chain entry %d: %w", index, ErrInvalidChain)
		}
	}
	chain := &Chain{}
	stores := make([]any, 0, len(paths))
	for index, name := range paths {
		v, err := open(name)
		if err != nil {
			err = fmt.Errorf("volume chain entry %d (%q): %w", index, name, err)
			return nil, errors.Join(err, chain.Close())
		}
		if v == nil {
			continue
		}
		chain.volumes = append(chain.volumes, v)
		chain.paths = append(chain.paths, name)
		stores = append(stores, v)
	}
	chain.CombinedBlockStore = *eris.NewCombinedBlockStore(stores...)
	return chain, nil
}

// Len returns the number of volumes in the chain.
func (chain *Chain) Len() int { return len(chain.volumes) }

// Paths returns a copy of the paths that opened successfully, in chain order.
func (chain *Chain) Paths() []string { return slices.Clone(chain.paths) }

// Close closes every volume in the chain, even if an earlier close
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

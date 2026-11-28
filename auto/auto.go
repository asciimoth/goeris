// Package auto opens the default ERIS stores for command-line tools and other
// applications. See [Open] for the paths and lookup order.
package auto

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/fs"
	"github.com/asciimoth/goeris/internal/streamio"
	"github.com/asciimoth/goeris/volume"
)

// Stores contains the configured block, capability, and ERIS-FS stores.
// Use [Open] to construct it. Close it after all operations on the stores
// have finished.
type Stores struct {
	Blocks       eris.BlockStore
	Capabilities eris.CapabilityStore
	Images       fs.ImageStore

	volumes                *volume.Chain
	capabilitiesFromBlocks bool
	capabilityMount        bool
	imagesFromCapabilities bool
	imageMount             bool
}

// DefaultSetup describes the default backends in a set of open stores.
// The mount roots are in lookup order. Each store tries its source chain
// before its mount roots.
type DefaultSetup struct {
	BlockVolumes           []string
	CapabilitiesFromBlocks bool
	CapabilityMountRoots   []string
	ImagesFromCapabilities bool
	ImageMountRoots        []string
}

// DefaultSetup reports the backends selected by Open, before any calls to
// Close. It excludes backends provided through options. Like the mount stores,
// it checks the mount environment on each call.
func (s *Stores) DefaultSetup() DefaultSetup {
	setup := DefaultSetup{
		CapabilitiesFromBlocks: s.capabilitiesFromBlocks,
		ImagesFromCapabilities: s.imagesFromCapabilities,
	}
	if s.volumes != nil {
		setup.BlockVolumes = s.volumes.Paths()
	}
	if s.capabilityMount {
		setup.CapabilityMountRoots = streamio.MountRoots("")
	}
	if s.imageMount {
		setup.ImageMountRoots = streamio.ImageMountRoots("")
	}
	return setup
}

// Open assembles the stores in dependency order:
//
//   - Blocks uses [volume.OpenAvailableChain] with ERIS_BLOCK_PATH. An empty
//     value selects [volume.DefaultChainPaths]. Missing volumes are skipped.
//     Selected platform default directories are created if possible.
//   - Capabilities uses the configured block backends, then reads from
//     ERIS_MNT or the default roots through [eris.NewMountCapabilityStore].
//   - Images uses the configured capability backends with zero block options,
//     then looks under ERIS_FS_MNT, the fs directory under ERIS_MNT, or the
//     default image roots through [fs.NewMountStore].
//
// Mounts resolve their environment paths on lookup. Volume paths are opened
// before Open returns. [ReplaceBlocks] skips opening the default volume chain.
func Open(options ...Option) (*Stores, error) {
	var cfg config
	for _, option := range options {
		if option != nil {
			option(&cfg)
		}
	}

	stores := &Stores{}
	var defaults []any
	if !cfg.blocks.replaced {
		volumes, err := volume.OpenAvailableChain(os.Getenv("ERIS_BLOCK_PATH"))
		if err != nil {
			return nil, fmt.Errorf("eris auto: open block volumes: %w", err)
		}
		if volumes.Len() > 0 {
			stores.volumes = volumes
			defaults = []any{volumes}
		}
	}
	blocks := cfg.blocks.entries(defaults...)
	stores.Blocks = eris.NewCombinedBlockStore(blocks...)
	if cfg.wrapBlocks != nil {
		stores.Blocks = cfg.wrapBlocks(stores.Blocks)
	}

	var capabilityDefaults []eris.CapabilityStore
	if source := capabilityBlocks(stores.Blocks, blocks); source != nil {
		capabilityDefaults = append(capabilityDefaults, blockCapabilities{eris.NewBlockCapabilityStore(source)})
		stores.capabilitiesFromBlocks = !cfg.capabilities.replaced
	}
	capabilityDefaults = append(capabilityDefaults, eris.NewMountCapabilityStore(""))
	stores.capabilityMount = !cfg.capabilities.replaced
	capabilities := cfg.capabilities.entries(capabilityDefaults...)
	stores.Capabilities = eris.NewCombinedCapabilityStore(capabilities...)

	var imageDefaults []fs.ImageStore
	if slices.ContainsFunc(capabilities, func(store eris.CapabilityStore) bool { return store != nil }) {
		imageDefaults = append(imageDefaults, fs.NewStore(stores.Capabilities, eris.BlockOptions{}))
		stores.imagesFromCapabilities = !cfg.images.replaced
	}
	imageDefaults = append(imageDefaults, fs.NewMountStore(""))
	stores.imageMount = !cfg.images.replaced
	stores.Images = fs.NewCombinedStore(cfg.images.entries(imageDefaults...)...)
	return stores, nil
}

// A sized block reader defers I/O. Check the first byte while the capability
// chain can still try the mount. ReadAt preserves the position and lets the
// reader reuse the block on the next read.
type blockCapabilities struct{ eris.CapabilityStore }

func (s blockCapabilities) NewReaderWithSize(ctx context.Context, cap eris.Capability, size int64, options eris.BlockOptions) (eris.CapabilityReader, error) {
	reader, err := s.CapabilityStore.NewReaderWithSize(ctx, cap, size, options)
	if err != nil {
		return nil, err
	}
	var first [1]byte
	_, err = reader.ReadAt(first[:], 0)
	if err == nil || (size == 0 && err == io.EOF) {
		return reader, nil
	}
	_ = reader.Close()
	return nil, err
}

// A read-only capability store can reject writes before it reads their input.
// Hide the combined store's sink methods when it has only block sources.
func capabilityBlocks(chain eris.BlockStore, entries []any) eris.BlockSource {
	var readable bool
	for _, entry := range entries {
		if _, ok := entry.(eris.BlockSink); ok {
			return chain
		}
		if _, ok := entry.(eris.BlockSource); ok {
			readable = true
		}
	}
	if readable {
		return struct{ eris.BlockSource }{chain}
	}
	return nil
}

// Close releases the volume handles opened by Open. The caller owns the
// stores supplied through options. Repeated calls to Close return nil.
func (s *Stores) Close() error {
	if s.volumes == nil {
		return nil
	}
	volumes := s.volumes
	s.volumes = nil
	return volumes.Close()
}

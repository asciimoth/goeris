package main

import (
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/auto"
	"github.com/asciimoth/goeris/fs"
	"github.com/asciimoth/goeris/volume"
)

// storageFlags holds the paths supplied for each of the three store chains.
// The zero value uses the default backends.
type storageFlags struct {
	blocks, capabilities, images storePaths
}

// A nil replacement keeps the defaults. A non-nil empty replacement
// removes them. Each group keeps its paths in lookup order.
type storePaths struct {
	before, replacement, after []string
}

func (paths *storageFlags) register(flags *flag.FlagSet) {
	paths.blocks.register(flags, "blocks")
	paths.capabilities.register(flags, "capabilities")
	paths.images.register(flags, "fs")
}

// register records literal paths without opening anything during parsing.
// Empty entries are invalid because an empty backend path selects defaults.
func (paths *storePaths) register(flags *flag.FlagSet, suffix string) {
	for _, group := range []struct {
		action string
		paths  *[]string
	}{
		{"prepend", &paths.before},
		{"replace", &paths.replacement},
		{"append", &paths.after},
	} {
		flags.Func(group.action+"-"+suffix, "", func(value string) error {
			if group.action == "replace" && value == "" {
				*group.paths = []string{}
				return nil
			}
			entries := strings.Split(value, ";")
			if slices.Contains(entries, "") {
				return errors.New("path list contains an empty entry")
			}
			if group.action == "replace" {
				*group.paths = entries
			} else {
				*group.paths = append(*group.paths, entries...)
			}
			return nil
		})
	}
}

// lookupOrder returns the ordered paths for a chain with no derived backend.
func (paths storePaths) lookupOrder(defaults []string) []string {
	if paths.replacement != nil {
		defaults = paths.replacement
	}
	return slices.Concat(paths.before, defaults, paths.after)
}

// commandStores owns the explicit volumes as well as the stores from auto.
// The mount backends open files on lookup and need no setup cleanup.
type commandStores struct {
	*auto.Stores
	volumes []*volume.Chain
}

// open applies the common flags through auto.Open. Explicit volumes must
// exist. If setup fails, Close releases any volumes already opened.
func (paths storageFlags) open(options ...auto.Option) (*commandStores, error) {
	stores := &commandStores{}
	for _, group := range []struct {
		flag   string
		paths  []string
		option func(...any) auto.Option
	}{
		{"prepend-blocks", paths.blocks.before, auto.PrependBlocks},
		{"replace-blocks", paths.blocks.replacement, auto.ReplaceBlocks},
		{"append-blocks", paths.blocks.after, auto.AppendBlocks},
	} {
		if group.paths == nil {
			continue
		}
		if len(group.paths) == 0 {
			options = append(options, group.option())
			continue
		}
		volumes, err := volume.OpenChain(strings.Join(group.paths, ";"))
		if err != nil {
			return nil, errors.Join(fmt.Errorf("open --%s: %w", group.flag, err), stores.Close())
		}
		stores.volumes = append(stores.volumes, volumes)
		options = append(options, group.option(volumes))
	}
	options = append(options, mountOptions(paths.capabilities, eris.NewMountCapabilityStore,
		auto.PrependCapabilities, auto.ReplaceCapabilities, auto.AppendCapabilities)...)
	options = append(options, mountOptions(paths.images, func(path string) fs.ImageStore { return fs.NewMountStore(path) },
		auto.PrependImages, auto.ReplaceImages, auto.AppendImages)...)
	var err error
	stores.Stores, err = auto.Open(options...)
	if err != nil {
		return nil, errors.Join(err, stores.Close())
	}
	return stores, nil
}

// mountOptions creates one backend for each explicit mount root.
func mountOptions[T any](paths storePaths, mount func(string) T, prepend, replace, appendStore func(...T) auto.Option) []auto.Option {
	var options []auto.Option
	for _, group := range []struct {
		paths  []string
		option func(...T) auto.Option
	}{
		{paths.before, prepend},
		{paths.replacement, replace},
		{paths.after, appendStore},
	} {
		if group.paths == nil {
			continue
		}
		stores := make([]T, 0, len(group.paths))
		for _, path := range group.paths {
			stores = append(stores, mount(path))
		}
		options = append(options, group.option(stores...))
	}
	return options
}

// Close releases all volume handles after the command finishes. It also
// accepts a partially opened commandStores after a setup error.
func (stores *commandStores) Close() error {
	var errs []error
	if stores.Stores != nil {
		errs = append(errs, stores.Stores.Close())
	}
	for _, chain := range stores.volumes {
		errs = append(errs, chain.Close())
	}
	stores.volumes = nil
	return errors.Join(errs...)
}

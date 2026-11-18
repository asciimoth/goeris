package fs

import (
	"context"
	"errors"
	stdfs "io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/volume"
)

// FuzzMountListingCache compares public listings with a separate slice-based
// LRU model across successes, failures, aliases, Sub views, and cache resets.
func FuzzMountListingCache(f *testing.F) {
	for _, sequence := range [][]byte{
		{}, {0, 13, 26, 0, 13, 26}, {4, 17, 30, 4, 17, 30},
		{3, 29, 26, 9, 35, 6, 10, 0, 13, 26, 11},
		{0, 13, 7, 26, 6, 10, 11, 12},
	} {
		for limit := uint8(0); limit < 4; limit++ {
			f.Add(sequence, limit, uint8(3)-limit)
		}
	}
	blocks, _ := mountFixture(f)
	caps := mountCacheCaps(f)
	f.Fuzz(func(t *testing.T, sequence []byte, capabilityLimit, blockLimit uint8) {
		if len(sequence) > 96 {
			t.Skip()
		}
		capLimit, rawLimit := int(capabilityLimit%4), int(blockLimit%4)
		ctx := context.Background()
		source := &mountObservedSource{BlockSource: blocks, t: t, ctx: ctx}
		base := NewMount(ctx, source, eris.BlockOptions{})
		m := withMountCache(t, base, capLimit, rawLimit)
		var capOrder, blockOrder []int // Least recently used first.
		var images [3]bool
		touch := func(order []int, id, limit int) []int {
			if limit == 0 {
				return nil
			}
			if at := slices.Index(order, id); at >= 0 {
				order = slices.Delete(order, at, at+1)
			}
			order = append(order, id)
			if len(order) > limit {
				order = order[1:]
			}
			return order
		}
		rootNames := func() []string {
			names := map[string]bool{"fs": true}
			for _, id := range capOrder {
				names[caps[id].String()] = true
			}
			for _, id := range blockOrder {
				hash := caps[id].Root.Reference.String()
				names[hash], names[hash[:3]] = true, true
			}
			var result []string
			for name := range names {
				result = append(result, name)
			}
			slices.Sort(result)
			return result
		}
		var snapshot stdfs.ReadDirFile
		var snapshotNames []string
		defer func() {
			if snapshot != nil {
				_ = snapshot.Close()
			}
		}()
		for _, step := range sequence {
			action, id, alias := int(step%13), int(step/13)%3, int(step/39)%4
			cap := caps[id]
			capName := mountCaseVariants(mountAliases(t, cap.String())[alias])[alias]
			blockName := mountCaseVariants(cap.Root.Reference.String())[alias]
			switch action {
			case 0, 1, 2:
				var err error
				switch action {
				case 0:
					_, err = m.Stat(capName)
				case 1:
					_, err = m.Lstat(capName)
				case 2:
					var file ReadSeekCloser
					file, err = m.OpenFile(capName)
					if err == nil {
						_ = file.Close()
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				capOrder = touch(capOrder, id, capLimit)
			case 3, 9:
				var err error
				if action == 3 {
					var file stdfs.File
					file, err = m.Open("Fs/" + capName)
					if err == nil {
						_ = file.Close()
					}
				} else {
					var sub stdfs.FS
					sub, err = m.Sub("FS")
					if err != nil {
						t.Fatal(err)
					}
					_, err = stdfs.ReadDir(sub, capName)
				}
				if id != 2 {
					if !errors.Is(err, ErrInvalidIndex) {
						t.Fatalf("invalid image: %v", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					capOrder = touch(capOrder, id, capLimit)
					images[id] = true
				}
			case 4, 5, 8:
				var err error
				switch action {
				case 4:
					_, err = m.ReadFile(blockName)
				case 5:
					_, err = m.Stat(blockName)
				case 8:
					parts := strings.Split(filepath.ToSlash(volume.BlockPath(cap.Root.Reference)), "/")
					var sub stdfs.FS
					sub, err = m.Sub(strings.ToLower(parts[0]))
					if err != nil {
						t.Fatal(err)
					}
					_, err = stdfs.ReadFile(sub, strings.Join(parts[1:], "/"))
				}
				if err != nil {
					t.Fatal(err)
				}
				blockOrder = touch(blockOrder, id, rawLimit)
			case 6:
				entries, err := m.ReadDir(".")
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if _, err := entry.Info(); err != nil {
						t.Fatal(err)
					}
				}
			case 7:
				source.fail = errors.New("fuzz storage failure")
				for _, name := range []string{capName, blockName, "fs/" + capName} {
					if _, err := m.Stat(name); !errors.Is(err, source.fail) {
						t.Fatalf("failed access: %v", err)
					}
				}
				source.fail = nil
			case 10:
				if snapshot != nil {
					_ = snapshot.Close()
				}
				file, err := m.Open(".")
				if err != nil {
					t.Fatal(err)
				}
				snapshot = file.(stdfs.ReadDirFile)
				snapshotNames = rootNames()
			case 11:
				if snapshot != nil {
					entries, err := snapshot.ReadDir(-1)
					if err != nil {
						t.Fatal(err)
					}
					names := make([]string, len(entries))
					for i, entry := range entries {
						names[i] = entry.Name()
					}
					if !slices.Equal(names, snapshotNames) {
						t.Fatalf("snapshot changed: %q != %q", names, snapshotNames)
					}
					_ = snapshot.Close()
					snapshot = nil
				}
				m = withMountCache(t, m, capLimit, rawLimit)
				capOrder, blockOrder = nil, nil
			case 12:
				if _, err := base.ReadFile(capName); err != nil {
					t.Fatal(err)
				}
				checkMountListing(t, base, ".", "fs")
			}
			var imageNames []string
			for i := range images {
				if !slices.Contains(capOrder, i) {
					images[i] = false
				}
				if images[i] {
					imageNames = append(imageNames, caps[i].String())
				}
			}
			checkMountListing(t, m, ".", rootNames()...)
			checkMountListing(t, m, "fs", imageNames...)
			// Prefix metadata is derived from retained hashes, not retained as an
			// independent set of directories that could grow beyond the cache limit.
			parts := strings.Split(filepath.ToSlash(volume.BlockPath(cap.Root.Reference)), "/")
			for depth := 1; depth <= 3; depth++ {
				prefix := strings.Join(parts[:depth], "/")
				names := map[string]bool{}
				for _, blockID := range blockOrder {
					p := strings.Split(filepath.ToSlash(volume.BlockPath(caps[blockID].Root.Reference)), "/")
					if slices.Equal(parts[:depth], p[:depth]) {
						names[p[depth]] = true
					}
				}
				var want []string
				for name := range names {
					want = append(want, name)
				}
				checkMountListing(t, m, strings.ToLower(prefix), want...)
			}
		}
	})
}

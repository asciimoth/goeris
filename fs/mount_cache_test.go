package fs

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/volume"
)

func withMountCache(t testing.TB, m *Mount, capabilities, blocks int) *Mount {
	t.Helper()
	result, err := m.WithListingCache(capabilities, blocks)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mountCacheCaps(t testing.TB) []eris.Capability {
	t.Helper()
	var result []eris.Capability
	for _, name := range []string{mountHello, mountReadme, mountIndex} {
		cap, err := eris.ParseURN(name)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, cap)
	}
	return result
}

func checkMountListing(t testing.TB, image stdfs.FS, name string, want ...string) {
	t.Helper()
	entries, err := stdfs.ReadDir(image, name)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(entries))
	for i, entry := range entries {
		got[i] = entry.Name()
	}
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("ReadDir(%q) = %q; want %q", name, got, want)
	}
}

func TestMountListingCacheOptIn(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	base := NewMount(context.Background(), blocks, eris.BlockOptions{})
	for _, bad := range []*Mount{nil, {}, {source: blocks}, NewMount(context.Background(), nil, eris.BlockOptions{})} {
		if _, err := bad.WithListingCache(1, 1); !errors.Is(err, stdfs.ErrInvalid) {
			t.Fatalf("invalid receiver: %v", err)
		}
	}
	for _, limits := range [][2]int{{-1, 1}, {1, -1}} {
		if _, err := base.WithListingCache(limits[0], limits[1]); !errors.Is(err, stdfs.ErrInvalid) {
			t.Fatalf("negative limit: %v", err)
		}
	}
	caps := mountCacheCaps(t)
	for _, limits := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {math.MaxInt, math.MaxInt}} {
		cached := withMountCache(t, base, limits[0], limits[1])
		if cached == base || base.listings != nil {
			t.Fatal("changed original mount")
		}
		checkMountListing(t, cached, ".", "fs")
		if _, err := cached.ReadFile(mountHello); err != nil {
			t.Fatal(err)
		}
		if _, err := cached.ReadFile(caps[0].Root.Reference.String()); err != nil {
			t.Fatal(err)
		}
		want := []string{"fs"}
		if limits[0] > 0 {
			want = append(want, mountHello)
		}
		if limits[1] > 0 {
			hash := caps[0].Root.Reference.String()
			want = append(want, hash, hash[:3])
		}
		checkMountListing(t, cached, ".", want...)
		checkMountListing(t, base, ".", "fs")
		disabled := withMountCache(t, cached, 0, 0)
		checkMountListing(t, disabled, ".", "fs")
		if disabled.listings != nil {
			t.Fatal("disabled view retains cache")
		}
		checkMountListing(t, cached, ".", want...)
		fresh := withMountCache(t, cached, 1, 1)
		checkMountListing(t, fresh, ".", "fs")
	}
}

func TestMountListingCacheLayout(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	m := withMountCache(t, NewMount(context.Background(), blocks, eris.BlockOptions{}), 3, 3)
	caps := mountCacheCaps(t)
	// All three files have been opened, but no index has been interpreted yet.
	for _, cap := range caps {
		alias := strings.ToLower(mountAliases(t, cap.String())[2])
		file, err := m.OpenFile(alias)
		if err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
	}
	checkMountListing(t, m, ".", "fs", mountHello, mountReadme, mountIndex)
	checkMountListing(t, m, "FS")
	if _, err := m.ReadFile("Fs/" + strings.ToLower(mountIndex) + "/latest.txt"); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, m, "fs", mountIndex)
	for _, cap := range caps {
		hash := cap.Root.Reference.String()
		if _, err := m.ReadFile(strings.ToLower(hash)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"fs", mountHello, mountReadme, mountIndex}
	files := []string{mountHello, mountReadme, mountIndex, "fs/" + mountIndex + "/hello.txt", "fs/" + mountIndex + "/latest.txt", "fs/" + mountIndex + "/docs/readme.txt"}
	for _, cap := range caps {
		ref := cap.Root.Reference
		hash := ref.String()
		want = append(want, hash, hash[:3])
		files = append(files, hash, filepath.ToSlash(volume.BlockPath(ref)))
		for depth := 1; depth <= 3; depth++ {
			parts := []string{hash[:3], hash[3:6], hash[6:9], hash[9:]}
			checkMountListing(t, m, strings.ToLower(strings.Join(parts[:depth], "/")), parts[depth])
		}
	}
	checkMountListing(t, m, ".", want...)
	if err := fstest.TestFS(m, files...); err != nil {
		t.Fatal(err)
	}
	// Marking an already known image as a regular content file must keep its
	// image status. Loading it again must keep its already known decoded size.
	if _, err := m.Lstat(mountIndex); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadDir("fs/" + mountIndex); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, m, "fs", mountIndex)
}

func TestMountListingCacheLRU(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	caps := mountCacheCaps(t)
	m := withMountCache(t, NewMount(context.Background(), blocks, eris.BlockOptions{}), 2, 2)
	for _, cap := range caps[:2] {
		if _, err := m.Stat(cap.String()); err != nil {
			t.Fatal(err)
		}
	}
	// Listings and their Info calls must not promote the older capability.
	entries, err := m.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, err := entry.Info(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Stat(mountIndex); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, m, ".", "fs", mountReadme, mountIndex)
	// A case/encoding alias refreshes the same canonical identity.
	if _, err := m.Lstat(strings.ToLower(mountAliases(t, mountReadme)[1])); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stat(mountHello); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, m, ".", "fs", mountHello, mountReadme)
	for _, cap := range caps[:2] {
		if _, err := m.ReadFile(cap.Root.Reference.String()); err != nil {
			t.Fatal(err)
		}
	}
	hashA, hashB, hashC := caps[0].Root.Reference.String(), caps[1].Root.Reference.String(), caps[2].Root.Reference.String()
	if _, err := m.Stat(strings.ToLower(filepath.ToSlash(volume.BlockPath(caps[0].Root.Reference)))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadFile(hashC); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, m, ".", "fs", mountHello, mountReadme, hashA, hashA[:3], hashC, hashC[:3])
	checkMountListing(t, m, hashB[:3]) // No retained prefix after its final block is evicted.
	// Accessing blocks must not change capability recency.
	if _, err := m.Stat(mountIndex); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, m, ".", "fs", mountHello, mountIndex, hashA, hashA[:3], hashC, hashC[:3])
	// Evicting an index also removes it from fs, without blocking direct lookup.
	imageCache := withMountCache(t, m, 1, 0)
	if _, err := imageCache.ReadDir("fs/" + mountIndex); err != nil {
		t.Fatal(err)
	}
	if _, err := imageCache.Stat(mountHello); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, imageCache, "fs")
	if _, err := imageCache.ReadDir("fs/" + mountIndex); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, imageCache, "fs", mountIndex)
}

func TestMountListingCacheViewsAndSnapshots(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	caps := mountCacheCaps(t)
	m := withMountCache(t, NewMount(context.Background(), blocks, eris.BlockOptions{}), 1, 1)
	namespace, err := m.Sub("FS")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdfs.ReadDir(namespace, strings.ToLower(mountIndex)); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, m, "fs", mountIndex)
	// Image Sub handles do not add internal file capabilities or decoder blocks.
	image, err := m.Sub("fs/" + mountIndex)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdfs.ReadFile(image, "hello.txt"); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, m, ".", "fs", mountIndex)
	// A cache can be enabled independently on an existing virtual Sub view.
	isolated := withMountCache(t, namespace.(*Mount), 1, 1)
	checkMountListing(t, isolated, ".")
	if _, err := isolated.ReadDir(mountIndex); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stat(mountHello); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, namespace, ".")
	checkMountListing(t, isolated, ".", mountIndex)
	root, err := m.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := m.Stat(mountReadme); err != nil {
		t.Fatal(err)
	}
	var oldNames []string
	for {
		entries, err := root.(stdfs.ReadDirFile).ReadDir(1)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			oldNames = append(oldNames, entry.Name())
			if _, err := entry.Info(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !slices.Equal(oldNames, []string{"fs", mountHello}) {
		t.Fatalf("directory snapshot changed: %v", oldNames)
	}
	checkMountListing(t, m, ".", "fs", mountReadme)
	hash := caps[0].Root.Reference.String()
	prefix, err := m.Sub(strings.ToLower(hash[:3]))
	if err != nil {
		t.Fatal(err)
	}
	relative := hash[3:6] + "/" + hash[6:9] + "/" + hash[9:]
	if _, err := stdfs.ReadFile(prefix, relative); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, prefix, ".", hash[3:6])
	directory, err := prefix.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	if _, err := m.ReadFile(caps[1].Root.Reference.String()); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, prefix, ".")
	old, err := directory.(stdfs.ReadDirFile).ReadDir(-1)
	if err != nil || len(old) != 1 || old[0].Name() != hash[3:6] {
		t.Fatalf("prefix snapshot: %v, %v", old, err)
	}
}

func TestMountListingCacheFailuresAndMetadata(t *testing.T) {
	blocks, _ := mountFixture(t)
	caps := mountCacheCaps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &mountObservedSource{BlockSource: blocks, t: t, ctx: ctx, options: eris.BlockOptions{HighPriority: true}}
	m := withMountCache(t, NewMount(ctx, source, source.options), 2, 2)
	for _, cap := range caps[:2] {
		if _, err := m.Stat(cap.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Stat(cap.Root.Reference.String()); err != nil {
			t.Fatal(err)
		}
	}
	source.fail = errors.New("unavailable")
	for _, name := range []string{mountHello, caps[0].Root.Reference.String(), "fs/" + mountIndex} {
		if _, err := m.Open(name); !errors.Is(err, source.fail) {
			t.Fatalf("cached data used: %v", err)
		}
	}
	source.fail = nil
	if _, err := m.Stat(mountIndex); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stat(caps[2].Root.Reference.String()); err != nil {
		t.Fatal(err)
	}
	// Failed accesses did not promote either older entry.
	hashB, hashC := caps[1].Root.Reference.String(), caps[2].Root.Reference.String()
	checkMountListing(t, m, ".", "fs", mountReadme, mountIndex, hashB, hashB[:3], hashC, hashC[:3])
	fresh := withMountCache(t, m, 2, 2)
	if _, err := fresh.Open("fs/" + mountHello); !errors.Is(err, ErrInvalidIndex) {
		t.Fatal(err)
	}
	checkMountListing(t, fresh, ".", "fs")
	// A successfully retrieved index is known even if a path inside is absent.
	if _, err := fresh.Open("fs/" + mountIndex + "/missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatal(err)
	}
	checkMountListing(t, fresh, "fs", mountIndex)
	// Metadata for an index-only capability can require I/O. Merely listing it
	// must not call storage, and Info must retain context and options.
	source.fail = errors.New("metadata unavailable")
	reads := source.reads
	entries, err := fresh.ReadDir(".")
	if err != nil || source.reads != reads {
		t.Fatalf("listing read storage: %v", err)
	}
	entry := entries[1]
	if entry.Name() != mountIndex {
		t.Fatalf("entry = %v", entry)
	}
	if _, err := entry.Info(); !errors.Is(err, source.fail) {
		t.Fatalf("metadata error: %v", err)
	}
	source.fail = nil
	info, err := entry.Info()
	if err != nil || info.Size() != 203 {
		t.Fatalf("index size: %v, %v", info, err)
	}
	// An earlier entry snapshot remains valid after its identity is evicted.
	if _, err := fresh.Stat(mountHello); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Stat(mountReadme); err != nil {
		t.Fatal(err)
	}
	if info, err := entry.Info(); err != nil || info.Size() != 203 {
		t.Fatalf("evicted metadata: %v, %v", info, err)
	}
	bad := withMountCache(t, NewMount(ctx, mountBadBlockSource{blocks, nil}, eris.BlockOptions{}), 1, 1)
	if _, err := bad.Open(hashB); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Fatal(err)
	}
	checkMountListing(t, bad, ".", "fs")
	cancel()
	for op, call := range mountOperations(m) {
		checkMountError(t, op, ".", call("."), context.Canceled)
	}
}

func TestMountListingCacheConcurrent(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	caps := mountCacheCaps(t)
	m := withMountCache(t, NewMount(context.Background(), blocks, eris.BlockOptions{}), 2, 2)
	var wg sync.WaitGroup
	for worker := range 12 {
		wg.Go(func() {
			for i := range 50 {
				cap := caps[(worker+i)%len(caps)]
				if _, err := m.Stat(cap.String()); err != nil {
					t.Error(err)
					return
				}
				if _, err := m.Stat(cap.Root.Reference.String()); err != nil {
					t.Error(err)
					return
				}
				if _, err := m.ReadDir("fs/" + mountIndex); err != nil {
					t.Error(err)
					return
				}
				for _, path := range []string{".", "FS", cap.Root.Reference.String()[:3]} {
					entries, err := m.ReadDir(path)
					if err != nil {
						t.Error(err)
						return
					}
					for _, entry := range entries {
						if _, err := entry.Info(); err != nil {
							t.Error(err)
							return
						}
					}
				}
				m.listings.mu.Lock()
				bounded := len(m.listings.capabilities.entries) <= 2 && len(m.listings.blocks.entries) <= 2
				m.listings.mu.Unlock()
				if !bounded {
					t.Error("cache exceeded bounds")
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestMountListingCacheSharedPrefixes(t *testing.T) {
	t.Parallel()
	// Fixed references let the listing cache test all three shared prefix levels
	// without a costly search for hashes with a 45-bit common prefix. No content
	// is loaded: this tests retained identities and metadata only.
	refs := []eris.Reference{{31: 1}, {31: 2}, {0: 255}, {0: 254}}
	source := &mountObservedSource{BlockSource: eris.BlockSet{}, t: t, ctx: context.Background(), fail: errors.New("listing must not fetch content")}
	m := withMountCache(t, NewMount(source.ctx, source, eris.BlockOptions{}), 0, 2)
	m.rememberBlock(refs[0], 1024)
	m.rememberBlock(refs[1], 32768)
	checkMountListing(t, m, ".", "fs", "AAA", refs[0].String(), refs[1].String())
	checkMountListing(t, m, "AAA", "AAA")
	checkMountListing(t, m, "AAA/AAA", "AAA")
	checkMountListing(t, m, "AAA/AAA/AAA", refs[0].String()[9:], refs[1].String()[9:])
	entries, err := m.ReadDir("AAA/AAA/AAA")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		size := int64(1024)
		if entry.Name() == refs[1].String()[9:] {
			size = 32768
		}
		if info.Size() != size || info.Mode() != 0444 {
			t.Fatalf("metadata = %v", info)
		}
	}
	m.rememberBlock(refs[2], 1024)
	checkMountListing(t, m, "AAA/AAA/AAA", refs[1].String()[9:])
	checkMountListing(t, m, "AAA", "AAA")
	m.rememberBlock(refs[3], 1024)
	checkMountListing(t, m, "AAA")
	checkMountListing(t, m, ".", "fs", refs[2].String()[:3], refs[2].String(), refs[3].String()[:3], refs[3].String())
	if source.reads != 0 {
		t.Fatal("listing or Info read storage")
	}
}

func TestMountDirectoryCloseReleasesSnapshot(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	m := withMountCache(t, NewMount(context.Background(), blocks, eris.BlockOptions{}), 3, 3)
	for _, cap := range mountCacheCaps(t) {
		if _, err := m.Stat(cap.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Stat(cap.Root.Reference.String()); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".", "fs/" + mountIndex} {
		opened, err := m.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		file := opened.(stdfs.ReadDirFile)
		if _, err := file.ReadDir(1); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		// A closed handle must not retain the snapshot or the image tree through
		// its node pointer. Both can be much larger than the handle itself.
		underlying := opened
		if named, ok := opened.(namedDirectory); ok {
			underlying = named.File
		}
		snapshot := underlying.(*directoryFile)
		if snapshot.node != nil || snapshot.entries != nil || snapshot.ctx != nil {
			t.Fatal("closed directory retained its tree, snapshot, or context")
		}
		if _, err := file.Stat(); !errors.Is(err, stdfs.ErrClosed) {
			t.Fatalf("closed Stat: %v", err)
		}
		if _, err := file.ReadDir(1); !errors.Is(err, stdfs.ErrClosed) {
			t.Fatalf("closed ReadDir: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMountIndexListingRefreshesRecency(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	m := withMountCache(t, NewMount(context.Background(), blocks, eris.BlockOptions{}), 2, 0)
	if _, err := m.ReadDir("fs/" + mountIndex); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stat(mountHello); err != nil {
		t.Fatal(err)
	}
	// This is an index load, unlike listing the virtual root or fs directory.
	if _, err := m.ReadDir("fs/" + mountIndex + "/docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stat(mountReadme); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, m, ".", "fs", mountIndex, mountReadme)
	checkMountListing(t, m, "fs", mountIndex)
}

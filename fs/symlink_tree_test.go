package fs

import (
	"errors"
	"fmt"
	stdfs "io/fs"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

// Each level has a file f with distinct content, a link l with the supplied
// target, a side branch b/l -> ../f, and a directory alias a -> d.
// Bit i puts the next directory in a separate index. Without that bit, d
// is an implicit directory in this index.
func symlinkTree(t *testing.T, targets []string, boundaries, legacy uint8) (*FS, *Mount, string) {
	t.Helper()
	ctx := t.Context()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := eris.NewBlockCapabilityStore(blocks)
	var encode func(int) eris.Capability
	encode = func(start int) eris.Capability {
		var index Index
		for level := start; level < len(targets); level++ {
			dir := strings.Repeat("d/", level-start)
			payload := []byte(fmt.Sprintf("file at level %d", level))
			file, err := store.Encode(ctx, payload, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			// Emit paths in CBOR key order for the legacy writer.
			index.Entries = append(index.Entries, Entry{Path: strings.Split(dir+"a", "/"), Type: Symlink, Target: "d"})
			nested := level+1 < len(targets) && boundaries&(1<<level) != 0
			if nested {
				index.Entries = append(index.Entries, Entry{Path: strings.Split(dir+"d", "/"), Type: Subdirectory, Capability: encode(level + 1)})
			}
			index.Entries = append(index.Entries,
				Entry{Path: strings.Split(dir+"f", "/"), Type: Regular, Capability: file, Size: int64(len(payload))},
				Entry{Path: strings.Split(dir+"l", "/"), Type: Symlink, Target: targets[level]},
				Entry{Path: strings.Split(dir+"b/l", "/"), Type: Symlink, Target: "../f"},
			)
			if nested {
				break
			}
		}
		if legacy&(1<<start) != 0 {
			return storeV1Index(t, index, blocks)
		}
		cap, err := EncodeIndex(ctx, index, eris.Secret{}, store, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return cap
	}
	cap := encode(0)
	image, err := OpenFS(ctx, cap, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return image, NewMount(ctx, blocks, eris.BlockOptions{}), "fs/" + cap.String() + "/"
}

func checkTreeRead(t *testing.T, name string, data []byte, err error, level int, wantErr error) {
	t.Helper()
	if wantErr != nil {
		if data != nil || !errors.Is(err, wantErr) {
			t.Fatalf("read %q = %q, %v; want no data and %v", name, data, err, wantErr)
		}
		return
	}
	want := fmt.Sprintf("file at level %d", level)
	if err != nil || string(data) != want {
		t.Fatalf("read %q = %q, %v; want %q", name, data, err, want)
	}
}

func TestSymlinkTreeReads(t *testing.T) {
	missing, invalid := stdfs.ErrNotExist, stdfs.ErrInvalid
	// Each visit follows a -> d, then returns to the index that owns l.
	// Include l itself in the 40-link budget.
	atLimit := strings.Repeat("a//./../", 39) + "f"
	for _, tc := range []struct {
		label      string
		targets    []string
		boundaries uint8
		name       string
		level      int
		readErr    error
		wantTarget string
		linkErr    error
	}{
		{"mixed index chain", []string{"d/l", "a/l", "./f"}, 3, "l", 2, nil, "d/l", nil},
		{"parent in same index", []string{"f", "f", "../f"}, 1, "a/d/l", 1, nil, "../f", nil},
		{"parent in separate index", []string{"f", "f", "../f"}, 3, "a/d/l", 0, missing, "", missing},
		{"outer target returns from nested index", []string{"a/d/../../f", "f", "f"}, 3, "l", 0, nil, "a/d/../../f", nil},
		{"nested target stops at own root", []string{"a/l", "../f"}, 1, "l", 0, missing, "", missing},
		{"directory link with caller suffix", []string{"d/d", "f", "f"}, 0, "l/l", 2, nil, "f", nil},
		{"sibling through parent", []string{"f", "../b/l"}, 0, "d/l", 0, nil, "../b/l", nil},
		{"sibling beyond boundary", []string{"f", "../b/l"}, 1, "d/l", 0, missing, "", missing},
		{"read through alias with different parent", []string{"d/d", "../l/../f", "f"}, 0, "d/l", 1, nil, "", missing},
		{"directory alias in target", []string{"./a//../d/f", "f"}, 0, "l", 1, nil, "./a//../d/f", nil},
		{"repeated nested alias at limit", []string{atLimit, "f"}, 1, "l", 0, nil, atLimit, nil},
		{"repeated nested alias over limit", []string{"a/../" + atLimit, "f"}, 1, "l", 0, invalid, "", invalid},
		{"dangling chain", []string{"a/l", "missing"}, 1, "l", 0, missing, "a/l", nil},
		{"file with trailing slash", []string{"f/"}, 0, "l", 0, missing, "f/", nil},
		{"empty target", []string{""}, 0, "l", 0, missing, "", nil},
		{"absolute target", []string{"d/l", "/f"}, 0, "l", 0, missing, "", missing},
		{"parent after missing component", []string{"absent/../f"}, 0, "l", 0, missing, "", missing},
		{"cycle across directories", []string{"d/l", "../l"}, 0, "l", 0, invalid, "", invalid},
		{"cycle cut by index boundary", []string{"d/l", "../l"}, 1, "l", 0, missing, "", missing},
	} {
		t.Run(tc.label, func(t *testing.T) {
			for _, legacy := range []uint8{0, 0x55, 0xff} {
				image, mount, root := symlinkTree(t, tc.targets, tc.boundaries, legacy)
				data, err := image.ReadFile(t.Context(), tc.name)
				checkTreeRead(t, tc.name, data, err, tc.level, tc.readErr)
				data, err = mount.ReadFile(root + tc.name)
				checkTreeRead(t, root+tc.name, data, err, tc.level, tc.readErr)
				target, err := mount.ReadLink(root + tc.name)
				if !errors.Is(err, tc.linkErr) || target != tc.wantTarget {
					t.Fatalf("ReadLink(%q) = %q, %v; want %q, %v", tc.name, target, err, tc.wantTarget, tc.linkErr)
				}
			}
		})
	}
}

// The graph has at most eight levels. Each edge visits at most two extra
// links, so only a cycle can exhaust the resolver's 40-link limit.
// The two errors describe the file read and the export of the target text.
// A missing file can still have a safe, readable link.
func expectedTreeRead(targets []string, boundaries uint8, level int) (int, error, error) {
	seen := make([]bool, len(targets))
	for !seen[level] {
		seen[level] = true
		switch target := targets[level]; target {
		case "./f", "b/l":
			return level, nil, nil
		case "d/l", "a/l":
			if level+1 == len(targets) {
				return 0, stdfs.ErrNotExist, nil
			}
			level++
		case "../f", "../l", "../../f", "../b/l":
			steps := 1
			if target == "../../f" {
				steps = 2
			}
			for range steps {
				if level == 0 || boundaries&(1<<(level-1)) != 0 {
					return 0, stdfs.ErrNotExist, stdfs.ErrNotExist
				}
				level--
			}
			if target != "../l" {
				return level, nil, nil
			}
		case "a//../f", "d/d/../../f":
			required := 1
			if target == "d/d/../../f" {
				required = 2
			}
			if level+required >= len(targets) {
				return 0, stdfs.ErrNotExist, stdfs.ErrNotExist
			}
			return level, nil, nil
		case "missing", "f/child":
			return 0, stdfs.ErrNotExist, nil
		case "/f":
			return 0, stdfs.ErrNotExist, stdfs.ErrNotExist
		case "l":
			return 0, stdfs.ErrInvalid, stdfs.ErrInvalid
		default:
			panic("missing test expectation for " + target)
		}
	}
	return 0, stdfs.ErrInvalid, stdfs.ErrInvalid
}

// Each byte selects the outgoing edge of one link. The expected read follows
// integer level numbers independently of the image resolver.
func FuzzSymlinkTreeReads(f *testing.F) {
	for _, edges := range [][]byte{{0}, {1, 1, 0}, {2, 3, 0}, {1, 4}, {5, 6}, {7, 8}, {9, 10}, {11, 0, 3}, {12, 13}} {
		for _, boundaries := range []uint8{0, 1, 0x55, 0xff} {
			f.Add(edges, boundaries, uint8(0x55))
		}
	}
	f.Fuzz(func(t *testing.T, edges []byte, boundaries, legacy uint8) {
		if len(edges) == 0 || len(edges) > 8 {
			t.Skip()
		}
		spellings := [...]string{"./f", "d/l", "a/l", "../f", "../l", "a//../f", "missing", "/f", "f/child", "l", "../../f", "d/d/../../f", "b/l", "../b/l"}
		targets := make([]string, len(edges))
		for i, edge := range edges {
			targets[i] = spellings[int(edge)%len(spellings)]
		}
		image, mount, root := symlinkTree(t, targets, boundaries, legacy)
		for start, target := range targets {
			level, readErr, linkErr := expectedTreeRead(targets, boundaries, start)
			name := strings.Repeat("d/", start) + "l"
			data, err := image.ReadFile(t.Context(), name)
			checkTreeRead(t, name, data, err, level, readErr)

			// Reach the same link through directory aliases in the mount.
			alias := root + strings.Repeat("a/", start) + "l"
			data, err = mount.ReadFile(alias)
			checkTreeRead(t, alias, data, err, level, readErr)
			got, err := mount.ReadLink(alias)
			if linkErr != nil {
				target = ""
			}
			if got != target || !errors.Is(err, linkErr) {
				t.Fatalf("ReadLink(%q) = %q, %v; want %q, %v", alias, got, err, target, linkErr)
			}
		}
	})
}

// A symlink may enter an index that is decoded only when first read.
// Its format error must reach the caller. Other links in the parent still work.
func TestReadThroughMalformedSymlinkIndex(t *testing.T) {
	ctx := t.Context()
	for label, target := range map[string][]byte{
		"non-text component":     {0x81, 0x01},
		"invalid UTF-8":          {0x81, 0x61, 0xff},
		"slash inside component": {0x81, 0x63, 'a', '/', 'b'},
		"truncated array":        {0x82, 0x61, 'f'},
	} {
		t.Run(label, func(t *testing.T) {
			store := newTestStore()
			// Version 2 with one entry: ["l"] maps to [Symlink, target].
			wire := append([]byte(MagicPrefix), 0x82, 2, 0xa1, 0x81, 0x61, 'l', 0x82, byte(Symlink))
			child, err := store.Encode(ctx, append(wire, target...), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			parent := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
			if err := parent.AddSubdirectory("child", child); err != nil {
				t.Fatal(err)
			}
			if err := parent.AddFile(ctx, "f", strings.NewReader("local"), false); err != nil {
				t.Fatal(err)
			}
			for name, dest := range map[string]string{"bad": "child/l", "good": "f"} {
				if err := parent.AddSymlink(name, dest); err != nil {
					t.Fatal(err)
				}
			}
			cap, err := parent.Finish(ctx)
			if err != nil {
				t.Fatal(err)
			}
			image, err := OpenFS(ctx, cap, store, eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if data, err := image.ReadFile(ctx, "bad"); !errors.Is(err, ErrInvalidIndex) || data != nil {
				t.Fatalf("ReadFile(bad) = %q, %v", data, err)
			}
			if text, err := image.ReadLink(ctx, "bad"); !errors.Is(err, ErrInvalidIndex) || text != "" {
				t.Fatalf("ReadLink(bad) = %q, %v", text, err)
			}
			if data, err := image.ReadFile(ctx, "good"); err != nil || string(data) != "local" {
				t.Fatalf("ReadFile(good) = %q, %v", data, err)
			}
		})
	}
}

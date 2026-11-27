package fs

import (
	"errors"
	"fmt"
	stdfs "io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/link"
)

// Record the destination when choosing an edge. The oracle never needs to
// interpret the target string or call the resolver to compute its answer.
type fixtureLink struct {
	name, target string
	dest         int // Directory containing the expected payload, or -1 for a rejected link.
	directory    bool
}

type fixtureDir struct {
	name    string
	parent  int
	index   int // Directory that owns the index for these entries.
	legacy  bool
	payload string
	links   []fixtureLink
}

type fixtureTree []fixtureDir

func belowFixtureDir(dir, name string) bool {
	return dir == "." || name == dir || strings.HasPrefix(name, dir+"/")
}

// Each input byte controls one directory. Keep a branch and a nested index
// even for an empty input. Further bytes vary tree depth, width and boundaries.
func validImageTree(input []byte) fixtureTree {
	n := min(len(input), 12)
	tree := make(fixtureTree, max(4, n))
	for i := range tree {
		var b byte
		if i < n {
			b = input[i]
		}
		d := &tree[i]
		d.name = "."
		if i > 0 {
			parent := int(b) % i
			switch i {
			case 2:
				parent = 1
			case 3:
				parent = 0
			}
			d.parent = parent
			d.name = path.Join(tree[parent].name, fmt.Sprintf("d%d", i))
			d.index = tree[parent].index
			if i == 1 || b&0x40 != 0 {
				d.index = i
			}
		}
		d.legacy = b&0x80 != 0
		d.payload = fmt.Sprintf("%s/%02x", d.name, b)
	}
	for i := range tree {
		d := &tree[i]
		var b byte
		if n > 0 {
			b = input[(i+1)%n]
		}
		// Every destination below the owning index is reachable. This also
		// permits a link in an outer index to enter another image.
		var candidates []int
		root := tree[d.index].name
		for j, other := range tree {
			if belowFixtureDir(root, other.name) {
				candidates = append(candidates, j)
			}
		}
		dest := candidates[int(b)%len(candidates)]
		rel, err := filepath.Rel(filepath.FromSlash(d.name), filepath.FromSlash(tree[dest].name))
		if err != nil {
			panic(err) // Both generated paths are relative.
		}
		rel = filepath.ToSlash(rel)
		local := "payload"
		if b&1 != 0 {
			local = "./payload"
		}
		via := rel + "/local"
		if b&2 != 0 {
			via = "./" + rel + "//local"
		}
		d.links = []fixtureLink{
			{"local", local, i, false},
			{"route", via, dest, false},
			{"alias", rel, dest, true},
			{"through", "alias/local", dest, false},
		}
	}
	tree[0].links = append(tree[0].links, fixtureLink{"selected", "local", 0, false})
	return tree
}

// Start with a valid tree so failures in a nested image cannot hide its
// other entries. Every step is a real link. The parent index contains a
// readable payload, so crossing the nested boundary would return real data.
func malformedImageTree(input []byte, variant byte) fixtureTree {
	tree := validImageTree(input)
	origin := 1 + int(variant>>3)%(len(tree)-1)
	d := &tree[origin]
	// Count parent edges to the index root, then take one more step.
	steps := 1
	for j := origin; j != d.index; j = tree[j].parent {
		steps++
	}
	parents := strings.Repeat("../", steps)
	targets := [...]string{parents + "payload", "/payload", "./" + parents + "local", "here/" + parents + "payload"}
	d.links = append(d.links, fixtureLink{"here", ".", origin, true})
	target := targets[variant%4]
	hops := 1 + int(variant>>2)%7
	for i := range hops {
		name := fmt.Sprintf("hop%d", i)
		d.links = append(d.links, fixtureLink{name, target, -1, false})
		target = "./" + name
	}
	links := tree[0].links
	links[len(links)-1] = fixtureLink{"selected", d.name + "/" + target, -1, false}
	return tree
}

// Encode each index separately, in child-first order. Implicit directories
// have no index entry. Both the legacy and current codecs read these images.
func (tree fixtureTree) encode(t testing.TB, blocks eris.BlockSet) eris.Capability {
	t.Helper()
	content := eris.NewBlockCapabilityStore(blocks)
	indexes := make([]Index, len(tree))
	var root eris.Capability
	for i := len(tree) - 1; i >= 0; i-- {
		d := tree[i]
		base := tree[d.index].name
		entryPath := func(name string) []string {
			full := path.Join(d.name, name)
			if base != "." {
				full = strings.TrimPrefix(full, base+"/")
			}
			return strings.Split(full, "/")
		}
		file, err := content.Encode(t.Context(), []byte(d.payload), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		idx := &indexes[d.index]
		idx.Entries = append(idx.Entries, Entry{Path: entryPath("payload"), Type: Regular, Size: int64(len(d.payload)), Capability: file})
		for _, link := range d.links {
			idx.Entries = append(idx.Entries, Entry{Path: entryPath(link.name), Type: Symlink, Target: link.target})
		}
		if d.index != i {
			continue
		}
		if d.legacy {
			root = storeV1Index(t, *idx, blocks)
		} else {
			root, err = EncodeIndex(t.Context(), *idx, eris.Secret{}, content, eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			break
		}
		parent := tree[d.parent].index
		relative := strings.TrimPrefix(d.name, tree[parent].name+"/")
		indexes[parent].Entries = append(indexes[parent].Entries, Entry{Path: strings.Split(relative, "/"), Type: Subdirectory, Capability: root})
	}
	return root
}

func fixtureBlocks() eris.BlockSet { return eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)} }

func checkFixtureFile(t *testing.T, view stdfs.FS, name, want string) {
	t.Helper()
	data, err := stdfs.ReadFile(view, name)
	if err != nil || string(data) != want {
		t.Fatalf("ReadFile(%q) = %q, %v; want %q", name, data, err, want)
	}
}

func checkRejectedLink(t *testing.T, view stdfs.ReadLinkFS, name string) {
	t.Helper()
	if target, err := view.ReadLink(name); target != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("ReadLink(%q) = %q, %v; want missing", name, target, err)
	}
	if data, err := stdfs.ReadFile(view, name); len(data) != 0 || !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("ReadFile(%q) = %q, %v; want missing", name, data, err)
	}
	if info, err := stdfs.Stat(view, name); info != nil || !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("Stat(%q) = %v, %v; want missing", name, info, err)
	}
	if entries, err := stdfs.ReadDir(view, name); entries != nil || !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("ReadDir(%q) = %v, %v; want missing", name, entries, err)
	}
	if file, err := view.Open(name); err == nil {
		_ = file.Close()
		t.Fatalf("Open(%q) succeeded", name)
	} else if !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("Open(%q): %v", name, err)
	}
}

func (tree fixtureTree) check(t *testing.T, view stdfs.ReadLinkFS, prefix string) {
	t.Helper()
	for _, d := range tree {
		dir := path.Join(prefix, d.name)
		entries, err := stdfs.ReadDir(view, dir)
		if err != nil {
			t.Fatalf("ReadDir(%q): %v", dir, err)
		}
		want := []string{"payload"}
		for _, link := range d.links {
			want = append(want, link.name)
		}
		for _, child := range tree[1:] {
			if path.Dir(child.name) == d.name {
				want = append(want, path.Base(child.name))
			}
		}
		slices.Sort(want)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if !slices.Equal(names, want) {
			t.Fatalf("ReadDir(%q) = %q; want %q", dir, names, want)
		}
		checkFixtureFile(t, view, path.Join(dir, "payload"), d.payload)
		for _, link := range d.links {
			name := path.Join(dir, link.name)
			info, err := view.Lstat(name)
			if err != nil || info.Mode()&stdfs.ModeSymlink == 0 {
				t.Fatalf("Lstat(%q) = %v, %v", name, info, err)
			}
			if link.dest < 0 {
				checkRejectedLink(t, view, name)
				continue
			}
			target, err := view.ReadLink(name)
			if err != nil || target != link.target {
				t.Fatalf("ReadLink(%q) = %q, %v; want %q", name, target, err, link.target)
			}
			payload := tree[link.dest].payload
			if link.directory {
				sub, err := stdfs.Sub(view, name)
				if err != nil {
					t.Fatalf("Sub(%q): %v", name, err)
				}
				checkFixtureFile(t, sub, "local", payload)
				name = path.Join(name, "payload")
			}
			checkFixtureFile(t, view, name, payload)
			info, err = stdfs.Stat(view, name)
			if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(payload)) {
				t.Fatalf("Stat(%q) = %v, %v", name, info, err)
			}
		}
	}
}

func checkEncodedTree(t *testing.T, tree fixtureTree) {
	t.Helper()
	blocks := fixtureBlocks()
	cap := tree.encode(t, blocks)
	view, err := OpenFS(t.Context(), cap, eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tree.check(t, NewIOFS(t.Context(), view), ".")
	mount := NewMount(t.Context(), blocks, eris.BlockOptions{})
	tree.check(t, mount, "fs/"+cap.String())
}

// Exercise both generators on CI platforms that run only regular tests.
func TestGeneratedImageTrees(t *testing.T) {
	for name, input := range map[string][]byte{
		"wide v2":    make([]byte, 12),
		"deep mixed": {128, 0, 128, 1, 3, 4, 5, 6, 7, 8, 9, 10},
		"nested v1":  {255, 255, 255, 255, 255, 255},
	} {
		t.Run(name, func(t *testing.T) {
			checkEncodedTree(t, validImageTree(input))
			for _, variant := range []byte{0, 9, 18, 255} {
				checkEncodedTree(t, malformedImageTree(input, variant))
			}
		})
	}
}

func FuzzValidImageTree(f *testing.F) {
	for _, data := range [][]byte{nil, {1, 2, 3, 4}, {255, 255, 255, 255}, {0, 65, 129, 192, 5, 11, 64, 73, 9, 8, 7, 6}} {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, input []byte) { checkEncodedTree(t, validImageTree(input)) })
}

func FuzzMalformedImageTree(f *testing.F) {
	for _, variant := range []byte{0, 1, 2, 3, 14, 27, 36, 255} {
		f.Add([]byte{0, 128, 67, 4, 169, 12}, variant)
	}
	f.Fuzz(func(t *testing.T, input []byte, variant byte) {
		checkEncodedTree(t, malformedImageTree(input, variant))
	})
}

// ReadFile uses the owning image boundary. ReadLink must also respect the
// directory exported by Sub. Check both rules through the same directory view.
func FuzzImageSubtreeLinks(f *testing.F) {
	for _, seed := range []byte{0, 1, 2, 3, 128, 255} {
		f.Add([]byte{1, 128, 3, 4, 5, 6}, seed)
	}
	f.Fuzz(func(t *testing.T, input []byte, choice byte) {
		tree := validImageTree(input)
		if choice&0x80 != 0 {
			tree = malformedImageTree(input, choice)
		}
		ctx := t.Context()
		blocks := fixtureBlocks()
		cap := tree.encode(t, blocks)
		store := NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
		a, err := store.OpenFS(ctx, cap)
		if err != nil {
			t.Fatal(err)
		}
		b, err := store.OpenFSWithLimit(ctx, cap, DefaultMaxIndexSize)
		if err != nil {
			t.Fatal(err)
		}
		base := NewIOFS(ctx, NewCombinedFileSystem(a, b))
		overlay, err := link.NewFS(base, eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		d := tree[int(choice)%len(tree)]
		// The same directory can also be selected inside an enclosing image.
		// Combine the two Sub views so ReadLink checks their shared location.
		wrapper, err := store.EncodeIndex(ctx, Index{Entries: []Entry{
			{Path: []string{"copy"}, Type: Subdirectory, Capability: cap},
		}}, eris.Secret{})
		if err != nil {
			t.Fatal(err)
		}
		parent, err := store.OpenFS(ctx, wrapper)
		if err != nil {
			t.Fatal(err)
		}
		embedded, err := parent.Sub(ctx, path.Join("copy", d.name))
		if err != nil {
			t.Fatal(err)
		}
		direct, err := b.Sub(ctx, d.name)
		if err != nil {
			t.Fatal(err)
		}
		views := []stdfs.FS{NewIOFS(ctx, NewCombinedFileSystem(embedded, direct))}
		for _, root := range []stdfs.FS{base, link.NewIOFS(ctx, overlay)} {
			view, err := stdfs.Sub(root, d.name)
			if err != nil {
				t.Fatalf("Sub(%q): %v", d.name, err)
			}
			views = append(views, view)
		}
		for _, view := range views {
			for _, l := range d.links {
				target, err := stdfs.ReadLink(view, l.name)
				if l.dest < 0 || !belowFixtureDir(d.name, tree[l.dest].name) {
					if target != "" || !errors.Is(err, stdfs.ErrNotExist) {
						t.Fatalf("Sub(%q).ReadLink(%q) = %q, %v; want missing", d.name, l.name, target, err)
					}
				} else if err != nil || target != l.target {
					t.Fatalf("Sub(%q).ReadLink(%q) = %q, %v; want %q", d.name, l.name, target, err, l.target)
				}
				name := l.name
				if l.directory {
					name = path.Join(name, "payload")
				}
				if l.dest < 0 {
					data, err := stdfs.ReadFile(view, name)
					if len(data) != 0 || !errors.Is(err, stdfs.ErrNotExist) {
						t.Fatalf("Sub(%q).ReadFile(%q) = %q, %v; want missing", d.name, name, data, err)
					}
				} else {
					checkFixtureFile(t, view, name, tree[l.dest].payload)
				}
			}
		}
	})
}

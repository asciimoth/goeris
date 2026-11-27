package fs

import (
	"errors"
	"fmt"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

// Materialize an immutable capability directory under a private test mount.
// Its caller supplies byte inputs with host-representable names and targets.
func mountFixtureMember(t testing.TB, tree fixtureTree, cap eris.Capability, state fixtureState) fixtureMember {
	t.Helper()
	if _, err := filepath.Localize(cap.String()); err != nil {
		t.Skipf("host cannot represent capability directory: %v", err)
	}
	mount := t.TempDir()
	m := fixtureMember{NewMountStore(mount), state, tree[0].payload, tree[1].payload, stdfs.ErrNotExist}
	if state == absentTree {
		return m
	}
	root := filepath.Join(mount, cap.String())
	for _, d := range tree {
		dir := filepath.Join(root, filepath.FromSlash(d.name))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload"), []byte(d.payload), 0600); err != nil {
			t.Fatal(err)
		}
		for _, l := range d.links {
			if err := os.Symlink(l.target, filepath.Join(dir, l.name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return m
}

// d3 is an implicit directory. Its route goes up to the parent and then
// down to d1/local. A decoder can follow it. A host mount must reject it.
func checkMixedParentLink(t *testing.T, chain *CombinedStore, cap eris.Capability, memory, host fixtureMember) {
	t.Helper()
	image, err := chain.OpenFS(t.Context(), cap)
	if err != nil {
		return
	} // checkFixtureChain verifies the open result.
	const name = "d3/route"
	data, err := image.ReadFile(t.Context(), name)
	if memory.openError() == nil && memory.state != brokenChild {
		if err != nil || string(data) != memory.childPayload {
			t.Fatalf("mixed ReadFile(%q) = %q, %v; want %q", name, data, err, memory.childPayload)
		}
	} else if len(data) != 0 || err == nil {
		t.Fatalf("mixed ReadFile(%q) = %q, %v; want failure", name, data, err)
	}
	// The mount either lacks the image or has no index marker for d3.
	if target, err := image.ReadLink(t.Context(), name); target != "" || err == nil {
		t.Fatalf("mixed ReadLink(%q) = %q, %v; want failure", name, target, err)
	}
	// Only the mount can return a host path. Check a chain of links both
	// where that mount contains the expected tree and where it rejects it.
	resolved, err := image.ResolvePath(t.Context(), "selected")
	if host.state != completeTree {
		if resolved != "" || err == nil {
			t.Fatalf("mixed ResolvePath = %q, %v; want failure", resolved, err)
		}
		return
	}
	want, errAbs := filepath.Abs(filepath.Join(host.store.(*MountStore).mount, cap.String(), "payload"))
	if errAbs != nil {
		t.Fatal(errAbs)
	}
	if err != nil || resolved != want {
		t.Fatalf("mixed ResolvePath = %q, %v; want %q", resolved, err, want)
	}
}

func TestGeneratedMountChains(t *testing.T) {
	input := []byte{1, 131, 5, 2, 97, 6, 7, 8}
	good, bad := validImageTree(input), malformedImageTree(input, 0)
	bad[0].payload = "other decoded tree"
	bank := buildFixtureStores(t, good, bad)
	for _, hostState := range []fixtureState{completeTree, absentTree, unsafeTree} {
		tree := good
		if hostState == unsafeTree {
			tree = bad
		}
		host := mountFixtureMember(t, tree, bank.request, hostState)
		if hostState != absentTree {
			image, err := host.store.OpenFS(t.Context(), bank.request)
			if err != nil {
				t.Fatal(err)
			}
			// The native tree has no nested-index markers. Its resolver
			// treats each containing directory as an image boundary.
			hostTree := make(fixtureTree, len(tree))
			for i, d := range tree {
				hostTree[i] = d
				hostTree[i].links = slices.Clone(d.links)
				for j, l := range d.links {
					if l.dest >= 0 && !belowFixtureDir(d.name, tree[l.dest].name) {
						hostTree[i].links[j].dest = -1
					}
				}
			}
			hostTree.check(t, NewIOFS(t.Context(), image), ".")
		}
		for _, memory := range bank.members {
			for _, mountFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("host%d/memory%d/hostFirst%t", hostState, memory.state, mountFirst), func(t *testing.T) {
					a, b := memory, host
					if mountFirst {
						a, b = b, a
					}
					chain := NewCombinedStore(a.store, b.store)
					checkFixtureChain(t, chain, bank.request, []fixtureMember{a, b})
					checkMixedParentLink(t, chain, bank.request, memory, host)
					// Write through the chain and reopen in the writable store.
					builder, err := chain.NewBuilder(eris.Secret{})
					if err != nil {
						t.Fatal(err)
					}
					if err := builder.AddFile(t.Context(), "written", strings.NewReader(t.Name()), false); err != nil {
						t.Fatal(err)
					}
					cap, err := builder.Finish(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					written, err := memory.store.OpenFS(t.Context(), cap)
					if err != nil {
						t.Fatal(err)
					}
					checkFixtureFile(t, NewIOFS(t.Context(), written), "written", t.Name())
					if _, err := host.store.OpenFS(t.Context(), cap); !errors.Is(err, stdfs.ErrNotExist) {
						t.Fatalf("new image on read-only mount: %v", err)
					}
				})
			}
		}
	}
}

// Use the same private, immutable host fixtures for every iteration in this
// worker. The fuzz input changes chain order, repetition, and nesting.
func FuzzMountedImageStoreChain(f *testing.F) {
	input := []byte{1, 131, 5, 2, 97, 6, 7, 8}
	good, bad := validImageTree(input), malformedImageTree(input, 0)
	bad[0].payload = "different mounted image"
	bank := buildFixtureStores(f, good, bad)
	members := append(slices.Clone(bank.members[:]),
		mountFixtureMember(f, good, bank.request, completeTree),
		mountFixtureMember(f, bad, bank.request, unsafeTree),
		mountFixtureMember(f, good, bank.request, absentTree))
	// Include every writable / mounted pair in both orders before mutation.
	for i := range bank.members {
		for j := len(bank.members); j < len(members); j++ {
			f.Add([]byte{byte(i), byte(j)})
			f.Add([]byte{byte(j), byte(i)})
		}
	}
	f.Fuzz(func(t *testing.T, layout []byte) {
		chain, order := buildFixtureChain(members, layout)
		checkFixtureChain(t, chain, bank.request, order)
	})
}

// A capability mount can also expose decoded index bytes and file contents
// as regular files. In that layout Store can enforce nested index boundaries.
func TestGeneratedCapabilityMount(t *testing.T) {
	ctx := t.Context()
	input := []byte{0, 128, 9, 3, 4, 133}
	for _, tree := range []fixtureTree{validImageTree(input), malformedImageTree(input, 3)} {
		blocks := fixtureBlocks()
		root := tree.encode(t, blocks)
		if _, err := filepath.Localize(root.String()); err != nil {
			t.Skipf("host cannot represent capability files: %v", err)
		}
		dir := t.TempDir()
		content := eris.NewBlockCapabilityStore(blocks)
		copyFile := func(cap eris.Capability) []byte {
			t.Helper()
			data, err := content.Decode(ctx, cap, eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, cap.String()), data, 0600); err != nil {
				t.Fatal(err)
			}
			return data
		}
		children := []eris.Capability{root}
		for len(children) > 0 {
			cap := children[0]
			children = children[1:]
			idx, err := ParseIndex(copyFile(cap))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range idx.Entries {
				switch e.Type {
				case Subdirectory:
					children = append(children, e.Capability)
				case Regular, Executable:
					copyFile(e.Capability)
				}
			}
		}
		mounted := NewStore(eris.NewMountCapabilityStore(dir), eris.BlockOptions{})
		image, err := mounted.OpenFS(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		tree.check(t, NewIOFS(ctx, image), ".")
	}
}

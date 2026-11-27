package fs

import (
	"context"
	"errors"
	stdfs "io/fs"
	"maps"
	"slices"
	"testing"

	"github.com/asciimoth/goeris"
)

// Substitute only the response for one root at the decoded-storage boundary.
// All child indexes, files, block reads, and parsing still use the real store.
type substitutedIndex struct {
	eris.CapabilityStore
	requested, supplied eris.Capability
}

func (s substitutedIndex) DecodeWithLimit(ctx context.Context, cap eris.Capability, limit int64, options eris.BlockOptions) ([]byte, error) {
	if cap == s.requested {
		cap = s.supplied
	}
	return s.CapabilityStore.DecodeWithLimit(ctx, cap, limit, options)
}

type fixtureState byte

const (
	completeTree fixtureState = iota
	absentTree
	unsafeTree
	brokenIndex
	missingPayload
	brokenChild
	stateCount
)

type fixtureMember struct {
	store                 ImageStore
	state                 fixtureState
	payload, childPayload string
	missing               error
}

type fixtureStores struct {
	request eris.Capability
	members [stateCount]fixtureMember
}

// Build the possible responses to a lookup of the same capability.
func buildFixtureStores(t testing.TB, good, bad fixtureTree) fixtureStores {
	t.Helper()
	var bank fixtureStores
	blocks := fixtureBlocks()
	bank.request = good.encode(t, blocks)
	add := func(state fixtureState, content eris.CapabilityStore, payload string) {
		bank.members[state] = fixtureMember{NewStore(content, eris.BlockOptions{}), state, payload, good[1].payload, eris.ErrMissingBlock}
	}
	content := eris.NewBlockCapabilityStore(blocks)
	add(completeTree, content, good[0].payload)
	other := fixtureBlocks()
	badRoot := bad.encode(t, other)
	otherContent := eris.NewBlockCapabilityStore(other)
	add(absentTree, otherContent, "") // Has a different image, but lacks the requested root.
	add(unsafeTree, substitutedIndex{otherContent, bank.request, badRoot}, bad[0].payload)
	truncated, err := otherContent.Encode(t.Context(), []byte(MagicPrefix), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	add(brokenIndex, substitutedIndex{otherContent, bank.request, truncated}, "")
	partial := eris.BlockSet{Blocks: maps.Clone(blocks.Blocks)}
	file, err := content.Encode(t.Context(), []byte(good[0].payload), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	delete(partial.Blocks, file.Root.Reference)
	add(missingPayload, eris.NewBlockCapabilityStore(partial), "")
	// Serve a well-formed root whose first nested index fails during lookup.
	index, err := DecodeIndex(t.Context(), bank.request, content, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child := slices.IndexFunc(index.Entries, func(e Entry) bool { return slices.Equal(e.Path, []string{good[1].name}) && e.Type == Subdirectory })
	if child < 0 {
		t.Fatal("generated root is missing its nested index")
	}
	broken, err := content.Encode(t.Context(), []byte(MagicPrefix), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	add(brokenChild, substitutedIndex{content, index.Entries[child].Capability, broken}, good[0].payload)
	return bank
}

// Each byte selects a backend. High bits optionally group the adjacent
// members into a nested chain. Keep the flat order as the lookup oracle.
func buildFixtureChain(members []fixtureMember, layout []byte) (*CombinedStore, []fixtureMember) {
	var stores []ImageStore
	var order []fixtureMember
	for _, code := range layout[:min(8, len(layout))] {
		m := members[int(code&0x7f)%len(members)]
		order = append(order, m)
		if code&0x80 != 0 && len(stores) > 0 {
			i := len(stores) - 1
			stores[i] = NewCombinedStore(stores[i], m.store)
		} else {
			stores = append(stores, m.store)
		}
	}
	return NewCombinedStore(stores...), order
}

func (m fixtureMember) openError() error {
	switch m.state {
	case absentTree:
		return m.missing
	case brokenIndex:
		return ErrInvalidIndex
	default:
		return nil
	}
}

func (m fixtureMember) result(name string, readLink bool) (string, error) {
	if err := m.openError(); err != nil {
		return "", err
	}
	if name == "d1/local" {
		if m.state == brokenChild {
			return "", ErrInvalidIndex
		}
		return m.childPayload, nil
	}
	if name == "selected" && m.state == unsafeTree {
		return "", stdfs.ErrNotExist
	}
	if readLink {
		return "local", nil
	}
	if m.state == missingPayload {
		return "", eris.ErrMissingBlock
	}
	return m.payload, nil
}

// OpenFS, ReadFile, and ReadLink can each succeed in a different member.
// Sub pins the first view that opens, including an image with missing leaves.
func checkFixtureChain(t *testing.T, chain *CombinedStore, cap eris.Capability, order []fixtureMember) {
	t.Helper()
	ctx := t.Context()
	image, err := chain.OpenFS(ctx, cap)
	var first *fixtureMember
	wantErr := ErrNoBackend
	for i := range order {
		wantErr = order[i].openError()
		if wantErr == nil {
			first = &order[i]
			break
		}
	}
	if !errors.Is(err, wantErr) || (image == nil) != (first == nil) {
		t.Fatalf("OpenFS = %T, %v; want %v", image, err, wantErr)
	}
	if first == nil {
		return
	}
	view := NewIOFS(ctx, image)
	mount := NewMountWithStore(ctx, chain, nil, eris.BlockOptions{})
	root := "fs/" + cap.String() + "/"
	for _, q := range []struct {
		name string
		link bool
	}{{"payload", false}, {"selected", false}, {"selected", true}, {"d1/local", false}} {
		var want string
		for _, m := range order {
			want, wantErr = m.result(q.name, q.link)
			if wantErr == nil {
				break
			}
		}
		check := func(v stdfs.ReadLinkFS, name string) {
			t.Helper()
			var got string
			var err error
			if q.link {
				got, err = v.ReadLink(name)
			} else {
				var data []byte
				data, err = stdfs.ReadFile(v, name)
				got = string(data)
			}
			if got != want || !errors.Is(err, wantErr) {
				t.Fatalf("%q (readlink=%v) = %q, %v; want %q, %v", name, q.link, got, err, want, wantErr)
			}
		}
		check(view, q.name)
		check(mount, root+q.name)
	}
	pinned, err := image.Sub(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	want, wantErr := first.result("selected", false)
	got, err := pinned.ReadFile(ctx, "selected")
	if string(got) != want || !errors.Is(err, wantErr) {
		t.Fatalf("Sub read = %q, %v; want %q, %v", got, err, want, wantErr)
	}
}

func FuzzImageStoreChain(f *testing.F) {
	for _, layout := range [][]byte{nil, {0}, {1}, {2}, {3}, {4}, {5}, {5, 0}, {0, 5}, {1, 2, 3, 4, 0}, {0, 2}, {4, 2, 1}, {3, 1}, {2, 128, 129, 130}, {0, 1, 2, 3, 4, 0, 1, 2}} {
		f.Add([]byte{1, 128, 3, 4, 5}, byte(0), layout)
	}
	f.Fuzz(func(t *testing.T, input []byte, variant byte, layout []byte) {
		good, bad := validImageTree(input), malformedImageTree(input, variant)
		bad[0].payload = "other/" + bad[0].payload
		bank := buildFixtureStores(t, good, bad)
		chain, order := buildFixtureChain(bank.members[:], layout)
		checkFixtureChain(t, chain, bank.request, order)
	})
}

package fs

import (
	"context"
	"errors"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/goeris"
)

// Use the index capability from the mount layout compatibility example.
func pathTestCapability(t testing.TB) eris.Capability {
	t.Helper()
	capability, err := eris.ParseURN("urn:eris:BIACGBUGVO7DG6RBLO75M3WY6JUXSVOKCD2C3NRSPKJKVHFZO6XBARY5ZN56UT4GIKHWGFKTUYQO7WFCNSRII5HBF42XLXZCUQSPQXTYCU")
	if err != nil {
		t.Fatal(err)
	}
	return capability
}

// makePathTestTree represents the decoded tree exposed by a compatible mount.
func makePathTestTree(t testing.TB, mount string, capability eris.Capability) string {
	t.Helper()
	// Windows cannot create literal URN file names.
	if _, err := filepath.Localize(capability.String()); err != nil {
		t.Skipf("host cannot represent capability file names: %v", err)
	}
	root := filepath.Join(mount, capability.String())
	writePathTestTree(t, root)
	return root
}

func writePathTestTree(t testing.TB, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"hello.txt": "Hello, ERIS!\n", "docs/readme.txt": "ERIS-FS example\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

type pathLookupStore struct {
	eris.CapabilityStore
	lookup func(context.Context, eris.Capability) (string, error)
}

func (s pathLookupStore) ResolvePath(ctx context.Context, capability eris.Capability) (string, error) {
	return s.lookup(ctx, capability)
}

func TestFSResolvePathStoreLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hostPath := filepath.Join(t.TempDir(), "content")
	blocks, _ := mountFixture(t)
	store := pathLookupStore{
		CapabilityStore: eris.NewBlockCapabilityStore(blocks),
		lookup: func(got context.Context, capability eris.Capability) (string, error) {
			if got != ctx {
				t.Fatal("lookup did not receive the caller's context")
			}
			switch capability.String() {
			case mountHello:
				return hostPath, nil
			case mountReadme:
				return "", eris.ErrMissingBlock
			default:
				t.Fatalf("unexpected lookup for %v", capability)
				return "", nil
			}
		},
	}
	image, err := NewStore(store, eris.BlockOptions{}).OpenFS(t.Context(), pathTestCapability(t))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := image.ResolvePath(ctx, "latest.txt"); got != hostPath || err != nil {
		t.Fatalf("ResolvePath through link = (%q, %v), want %q", got, err, hostPath)
	}
	const missing = "docs/readme.txt"
	got, err := image.ResolvePath(ctx, missing)
	var pe *stdfs.PathError
	if got != "" || !errors.As(err, &pe) || pe.Op != "resolvepath" || pe.Path != missing ||
		!errors.Is(err, eris.ErrMissingBlock) || errors.Is(err, ErrNoHostPath) {
		t.Fatalf("ResolvePath(%q) = (%q, %v), want missing content", missing, got, err)
	}
}

func TestFSResolvePathErrors(t *testing.T) {
	ctx := t.Context()
	root, blocks := contextImage(t)
	image, err := OpenFS(ctx, root, eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, tc := range []struct {
		ctx        context.Context
		name       string
		want       error
		noHostPath bool
	}{
		{ctx, ".", ErrNoHostPath, true},
		{ctx, "nested", ErrNoHostPath, true},
		{ctx, "nested/compressed", ErrNoHostPath, true},
		{ctx, "nested/link", errors.ErrUnsupported, true},
		{ctx, "nested/missing", stdfs.ErrNotExist, false},
		{ctx, "../file", stdfs.ErrInvalid, false},
		{canceled, "nested/file", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := image.ResolvePath(tc.ctx, tc.name)
			var pe *stdfs.PathError
			if got != "" || !errors.As(err, &pe) || pe.Op != "resolvepath" || pe.Path != tc.name {
				t.Fatalf("ResolvePath(%q) = (%q, %v)", tc.name, got, err)
			}
			if !errors.Is(err, tc.want) || errors.Is(err, ErrNoHostPath) != tc.noHostPath {
				t.Fatalf("got %v, want %v (ErrNoHostPath = %t)", err, tc.want, tc.noHostPath)
			}
			if tc.want == ErrNoHostPath && errors.Is(err, errors.ErrUnsupported) {
				t.Fatal("entry was passed to the block store")
			}
		})
	}
}

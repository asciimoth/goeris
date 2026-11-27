package fetch

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
)

const emptyTreeCap = "urn:eris:BIAMBMAQBTL6WWRPPQRYWPQFOPBX27WPOWGLPWNYCO5X2FRMHHWRYAD4K3ORFSM7JNOMEJWDIBEG5VAQ6ULESJB6NB7U7Z26GYQXZRYYNA"

func TestTreeVectors(t *testing.T) {
	// Generate the 1024-byte xorshift32 input from the ERIS-FS-AUTO-1 vectors.
	var random [1024]byte
	x := uint32(1)
	for i := range random {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		random[i] = byte(x)
	}
	for _, tc := range []struct {
		name, target, profile, expected string
		data                            []byte
	}{
		{profile: "none", expected: emptyTreeCap},
		{"hello", "", "none", "urn:eris:BIANO4IXQY4XU6EWRTRPBF6SMPKFQ5HUSYMIL2V7WNTBDS5WK7E4E32FKMDUIODQOEM2QUYS37TMRGZ6GCLKOR36VQQMGVU4LFVSR3CUJU", []byte("Hello world!")},
		{"l", "/a//b/", "none", "urn:eris:BIAPFENWSCHVP33WLV5C52NICNFOBGU4HCE6QWN5DZW3B7AKMZ5XZ5R5NWT46WK5BGG4LDYKZKVRVWB7LQBGUJ4LARAXMJDJM7L72DPGRI", nil},
		{"file", "", "auto", "urn:eris:BIACZ5E57FY2IDGF7I34DWENBOVRH3B4SWPEWEO2D3XPPVZOR5MNHD4MKZ43NFF5KRRCCLMMC6RVZHGEZKO7ACCP55TH2QIE5WVUWIJ53M", make([]byte, 1024)},
		{"file", "", "auto", "urn:eris:BIAOWCYFF3SO3ZVEXC7WGJIL5RWP4LWFMXKK6D2NIUT75LKYCOJCGDCTP2MWYXAUNJG5CPAZ2A5ZKQR4WFH5H6KYHSUUECFBWUJC7C2PIM", random[:]},
	} {
		o := Options{Kind: "erisfs", Compression: tc.profile}
		tree, err := o.NewTree(eris.NewBlockCapabilityStore(eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}), eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if tc.target != "" {
			err = tree.AddSymlink(tc.name, tc.target)
		} else if tc.name != "" {
			err = tree.AddFile(t.Context(), tc.name, iotest.DataErrReader(iotest.OneByteReader(bytes.NewReader(tc.data))), false)
		}
		if err != nil {
			t.Fatal(err)
		}
		cap, err := tree.Finish(t.Context())
		if err != nil || cap.String() != tc.expected {
			t.Fatalf("%s tree with %q: got %v, want %s, error %v", tc.profile, tc.name, cap, tc.expected, err)
		}
	}
}

func TestTreeBoundaries(t *testing.T) {
	store := eris.NewBlockCapabilityStore(eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}})
	for _, tc := range []struct {
		options Options
		key     string
	}{
		{Options{Kind: "blob"}, "erisfs.use"},
		{Options{Kind: "erisfs", Compression: "lzip-v1"}, "erisfs.compression"},
	} {
		if _, err := tc.options.NewTree(store, eris.BlockOptions{}); !errors.Is(err, Failure{"unsupported-option", tc.key}) {
			t.Fatalf("NewTree(%+v): %v", tc.options, err)
		}
	}
	o := Options{Kind: "erisfs", Compression: "auto"}
	if _, err := o.NewTree(nil, eris.BlockOptions{}); !errors.Is(err, erisfs.ErrInvalidEntry) {
		t.Fatalf("NewTree with nil store: %v", err)
	}
	tree, err := o.NewTree(store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.AddFile(t.Context(), "file", iotest.ErrReader(io.ErrUnexpectedEOF), false); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	if err := tree.AddFile(t.Context(), "file", nil, false); err == nil {
		t.Fatal("nil content")
	}
	if err := tree.AddFile(t.Context(), "f\x00", strings.NewReader(""), false); err == nil {
		t.Fatal("NUL in file path")
	}
	if tree.AddSymlink("f\x00", "x") == nil || tree.AddSymlink("f", "x\x00") == nil {
		t.Fatal("NUL in symlink")
	}
	if err := tree.AddFile(t.Context(), "file", bytes.NewReader(make([]byte, 1023)), true); err != nil {
		t.Fatal(err)
	}
	entry := tree.builder.Index().Entries[0]
	if entry.Size != 1023 || entry.Compression != erisfs.Uncompressed || entry.Type != erisfs.Executable {
		t.Fatal(entry)
	}
	if err := tree.AddSymlink("file/child", ""); !errors.Is(err, erisfs.ErrPathConflict) {
		t.Fatal(err)
	}
}

func TestTreeChecksPathBeforeReading(t *testing.T) {
	for _, profile := range []string{"none", "auto"} {
		t.Run(profile, func(t *testing.T) {
			options := Options{Kind: "erisfs", Compression: profile}
			tree, err := options.NewTree(eris.NewBlockCapabilityStore(eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}), eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := tree.AddSymlink("taken", "target"); err != nil {
				t.Fatal(err)
			}
			for path, want := range map[string]error{"../file": erisfs.ErrInvalidPath, "taken/child": erisfs.ErrPathConflict} {
				input := iotest.ErrReader(errors.New("unexpected read"))
				if err := tree.AddFile(t.Context(), path, input, false); !errors.Is(err, want) {
					t.Errorf("AddFile(%q) = %v, want %v", path, err, want)
				}
			}
		})
	}
}

func TestCompressedFileRetry(t *testing.T) {
	options, err := source(t, opening+"name file\nerisfs.compression auto\n").Validate(true)
	if err != nil {
		t.Fatal(err)
	}
	store := eris.NewBlockCapabilityStore(eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}})
	tree, err := options.NewTree(store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The stream fails after the builder has selected compression.
	want := bytes.Repeat([]byte("complete file\n"), 3000)
	partial := io.MultiReader(bytes.NewReader(want[:erisfs.AutoCompressionSampleSize+1]), iotest.ErrReader(io.ErrUnexpectedEOF))
	if err := tree.AddFile(t.Context(), "file", partial, false); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial file: %v", err)
	}
	if err := tree.AddFile(t.Context(), "file", bytes.NewReader(want), false); err != nil {
		t.Fatalf("retry: %v", err)
	}
	root, err := tree.Finish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	image, err := erisfs.OpenFS(t.Context(), root, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := image.ReadFile(t.Context(), "file")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes, want %d with identical content: %v", len(got), len(want), err)
	}
}

func TestTreeConvergenceSecret(t *testing.T) {
	options, err := source(t, opening+"name hello\neris.secret "+strings.Repeat("ab", 32)).Validate(true)
	if err != nil {
		t.Fatal(err)
	}
	blocks := eris.BlockOptions{}
	store := eris.NewBlockCapabilityStore(eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}})
	tree, err := options.NewTree(store, blocks)
	if err != nil {
		t.Fatal(err)
	}
	// The default stores the original bytes even when the handler supports auto.
	data := strings.Repeat("A", 1024)
	ctx := t.Context()
	if err := tree.AddFile(ctx, "hello", strings.NewReader(data), false); err != nil {
		t.Fatal(err)
	}
	got, err := tree.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var secret eris.Secret
	for i := range secret {
		secret[i] = 0xab
	}
	file, err := store.EncodeContentRecommended(ctx, strings.NewReader(data), secret, blocks)
	if err != nil {
		t.Fatal(err)
	}
	index := erisfs.Index{Entries: []erisfs.Entry{{
		Path: []string{"hello"}, Type: erisfs.Regular, Capability: file, Size: int64(len(data)),
	}}}
	want, err := erisfs.EncodeIndex(ctx, index, secret, store, blocks)
	if err != nil || got != want {
		t.Fatalf("tree with explicit convergence secret: got %v, want %v, error %v", got, want, err)
	}
}

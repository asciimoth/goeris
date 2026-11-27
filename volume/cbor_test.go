package volume_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sync"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/cbor"
	"github.com/asciimoth/goeris/volume"
)

func cborBlocks(t *testing.T) ([]cbor.Element, eris.BlockSet) {
	t.Helper()
	small, smallRef := testBlock(t, eris.BlockSize1KiB, 42)
	large, largeRef := testBlock(t, eris.BlockSize32KiB, 43)
	standalone, err := cbor.NewBlock(small)
	if err != nil {
		t.Fatal(err)
	}
	blockMap, err := cbor.NewBlockMap(eris.BlockSet{Blocks: map[eris.Reference]eris.Block{largeRef: {Data: large}}})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := eris.NewBlockCapabilityStore(eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}).Encode(t.Context(), []byte("reference only"), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capabilityElement, err := cbor.NewCapability(capability)
	if err != nil {
		t.Fatal(err)
	}
	return []cbor.Element{cbor.NewReference(smallRef), capabilityElement, standalone, blockMap}, eris.BlockSet{Blocks: map[eris.Reference]eris.Block{smallRef: {Data: small}, largeRef: {Data: large}}}
}

func TestCBORRootFormats(t *testing.T) {
	elements, blocks := cborBlocks(t)
	for _, format := range []struct {
		name    string
		marshal func([]cbor.Element) ([]byte, error)
	}{
		{"array", cbor.Marshal},
		{"untagged array", cbor.MarshalUntagged},
		{"self described", cbor.MarshalSelfDescribed},
		{"sequence", cbor.MarshalSequence},
		{"untagged sequence", cbor.MarshalUntaggedSequence},
		{"indefinite array", func(elements []cbor.Element) ([]byte, error) {
			data, err := cbor.MarshalUntaggedSequence(elements)
			return append(append([]byte{0x9f}, data...), 0xff), err
		}},
	} {
		t.Run(format.name, func(t *testing.T) {
			data, err := format.marshal(elements)
			if err != nil {
				t.Fatal(err)
			}
			// Detection is based on the root type, not the extension.
			name := filepath.Join(t.TempDir(), "blocks.data")
			writeFile(t, name, data)
			v := openVolume(t, name)
			for ref, want := range blocks.Blocks {
				for range 2 {
					got, err, _ := eris.GetBlock(context.Background(), v, ref, 1, eris.BlockOptions{})
					if err != nil || !bytes.Equal(got, want.Data) {
						t.Fatalf("GetBlock: length %d, %v", len(got), err)
					}
					got[0]++ // A returned block must not affect later reads.
				}
				if _, err := eris.PutBlock(context.Background(), v, want.Data, eris.NoTTL, eris.BlockOptions{}, ref); !errors.Is(err, volume.ErrNotDirectory) {
					t.Fatalf("write to CBOR root: %v", err)
				}
			}
			if _, err, _ := eris.GetBlock(context.Background(), v, eris.Reference{}, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, eris.ErrMissingBlock) || !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("missing block: %v", err)
			}
			assertFile(t, name, data)
		})
	}
}

func TestCBORRootInvalidData(t *testing.T) {
	elements, blocks := cborBlocks(t)
	data, err := cbor.Marshal(elements)
	if err != nil {
		t.Fatal(err)
	}
	corruptMap := bytes.Clone(data)
	corruptMap[len(corruptMap)-1] ^= 1
	ref, _ := elements[0].Reference() // A valid block before the corrupt map.
	for _, invalid := range [][]byte{
		[]byte("not CBOR"),
		data[:len(data)-1],
		append(bytes.Clone(data), 0x00),
		corruptMap,
		blocks.Blocks[ref].Data, // A bare raw block file is not a CBOR container.
	} {
		name := filepath.Join(t.TempDir(), "blocks.cbor")
		writeFile(t, name, invalid)
		v := openVolume(t, name)
		if got, err, _ := eris.GetBlock(context.Background(), v, ref, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, cbor.ErrInvalidEncoding) || got != nil {
			t.Fatalf("invalid CBOR root: length %d, %v", len(got), err)
		}
		if _, err := eris.PutBlock(context.Background(), v, blocks.Blocks[ref].Data, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, volume.ErrNotDirectory) {
			t.Fatalf("write to invalid CBOR root: %v", err)
		}
		assertFile(t, name, invalid)
	}
}

func TestCBORRootReferencesAreNotBlocks(t *testing.T) {
	elements, _ := cborBlocks(t)
	ref, _ := elements[0].Reference()
	metadata, err := cbor.Marshal(elements[:2])
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{metadata, nil, {0x80}} {
		name := filepath.Join(t.TempDir(), "metadata.cbor")
		writeFile(t, name, data)
		v := openVolume(t, name)
		if _, err, _ := eris.GetBlock(context.Background(), v, ref, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, eris.ErrMissingBlock) {
			t.Fatalf("metadata or empty container: %v", err)
		}
	}
}

func TestCBORRootConcurrentReads(t *testing.T) {
	elements, blocks := cborBlocks(t)
	data, err := cbor.Marshal(elements)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "blocks.cbor")
	writeFile(t, name, data)
	v := openVolume(t, name)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 10 {
				for ref, want := range blocks.Blocks {
					got, err, _ := eris.GetBlock(context.Background(), v, ref, eris.NoTTL, eris.BlockOptions{})
					if err != nil || !bytes.Equal(got, want.Data) {
						t.Errorf("concurrent CBOR read: length %d, %v", len(got), err)
						return
					}
				}
			}
		})
	}
	group.Wait()
}

func TestCBORRootCancellationAndClose(t *testing.T) {
	name := filepath.Join(t.TempDir(), "empty.cbor")
	writeFile(t, name, nil)
	v := openVolume(t, name)
	block, ref := testBlock(t, eris.BlockSize1KiB, 12)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err, _ := eris.GetBlock(ctx, v, ref, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
	if _, err := eris.PutBlock(ctx, v, block, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err, _ := eris.GetBlock(context.Background(), v, ref, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("closed read: %v", err)
	}
	if _, err := eris.PutBlock(context.Background(), v, block, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("closed write: %v", err)
	}
}

func TestDirectoryDoesNotScanCBORFiles(t *testing.T) {
	elements, _ := cborBlocks(t)
	data, err := cbor.Marshal(elements)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "blocks.cbor"), data)
	v := openVolume(t, root)
	ref, _ := elements[0].Reference()
	if _, err, _ := eris.GetBlock(context.Background(), v, ref, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("read an unrelated CBOR file inside a directory: %v", err)
	}
	assertFile(t, filepath.Join(root, "blocks.cbor"), data)
}

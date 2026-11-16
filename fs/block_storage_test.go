package fs_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
)

// opaqueBlocks has no file system methods and does not use eris.BlockSet.
// Separate handles show that the writer and reader need only block methods.
type opaqueBlocks struct {
	data         map[eris.Reference][]byte
	reads        int
	writes       int
	wantOptions  eris.BlockOptions
	wrongOptions bool
}

func (store *opaqueBlocks) PutBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, expected ...eris.Reference) (eris.Reference, error) {
	ref, err := eris.ReferenceOf(block)
	if err != nil {
		return eris.Reference{}, err
	}
	if len(expected) > 1 || (len(expected) == 1 && expected[0] != ref) {
		return eris.Reference{}, eris.ErrInvalidBlock
	}
	store.data[ref] = bytes.Clone(block)
	store.writes++
	store.wrongOptions = store.wrongOptions || options != store.wantOptions
	return ref, nil
}

func (store *opaqueBlocks) UpdateBlockOptions(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error) {
	return false, nil
}

//nolint:staticcheck // ST1008: BlockSource requires options as the third return value.
func (store *opaqueBlocks) GetBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	store.reads++
	store.wrongOptions = store.wrongOptions || options != store.wantOptions
	block, ok := store.data[ref]
	if !ok {
		return nil, eris.ErrMissingBlock, eris.BlockOptions{}
	}
	return bytes.Clone(block), nil, eris.BlockOptions{}
}

func TestImageThroughBlockStorage(t *testing.T) {
	options := eris.BlockOptions{HighPriority: true}
	store := &opaqueBlocks{data: make(map[eris.Reference][]byte), wantOptions: options}
	var _ eris.BlockStore = store

	plain := bytes.Repeat([]byte("a block storage image "), 4000)
	packed := strings.Repeat("compress this text ", 4000)
	nested := erisfs.NewBuilder(store, eris.Secret{1}, options)
	if err := nested.AddFile(context.Background(), "plain", bytes.NewReader(plain), false); err != nil {
		t.Fatal(err)
	}
	nestedCap, err := nested.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	builder := erisfs.NewBuilder(store, eris.Secret{1}, options)
	if err := builder.AddSubdirectory("nested", nestedCap); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddAutoFile(context.Background(), "packed", strings.NewReader(packed), false); err != nil {
		t.Fatal(err)
	}
	if builder.Index().Entries[1].Compression != erisfs.Lzip {
		t.Fatal("automatic compression did not select Lzip")
	}
	if err := builder.AddSymlink("link", "nested/plain"); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.writes < 3 {
		t.Fatalf("stored only %d blocks", store.writes)
	}

	// Read through a separate handle, as with an external block service.
	reader := &opaqueBlocks{data: store.data, wantOptions: options}
	image, err := erisfs.OpenFS(context.Background(), capability, reader, options)
	if err != nil {
		t.Fatal(err)
	}
	if reader.reads == 0 {
		t.Fatal("opening the index did not use GetBlock")
	}
	reader.reads = 0
	file, err := image.OpenFile(context.Background(), "nested/plain")
	if err != nil {
		t.Fatal(err)
	}
	if reader.reads == 0 {
		t.Fatal("nested index did not use GetBlock")
	}
	reader.reads = 0
	var sample [17]byte
	if _, err := file.ReadAt(sample[:], 40000); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sample[:], plain[40000:40017]) {
		t.Fatal("random-access content differs")
	}
	if reader.reads == 0 || reader.reads > 2 {
		t.Fatalf("random access fetched %d blocks", reader.reads)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{
		"link":   plain,
		"packed": []byte(packed),
	} {
		opened, err := image.Open(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(opened)
		closeErr := opened.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: read %v, close %v, content equal %v", name, readErr, closeErr, bytes.Equal(got, want))
		}
	}
	if reader.reads == 0 {
		t.Fatal("file reads did not use GetBlock")
	}
	sub, err := image.Sub(context.Background(), "nested")
	if err != nil {
		t.Fatal(err)
	}
	subFile, err := sub.Open(context.Background(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(subFile); err != nil {
		t.Fatal(err)
	}
	if err := subFile.Close(); err != nil {
		t.Fatal(err)
	}
	if reader.wrongOptions || store.wrongOptions {
		t.Fatal("file system did not forward construction options")
	}
}

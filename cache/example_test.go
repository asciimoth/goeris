package cache_test

import (
	"context"
	"fmt"
	"io"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/cache"
)

func ExampleNew() {
	capability, blocks, err := eris.Encode([]byte("Hello, cached ERIS!"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		panic(err)
	}
	// BlockSet supports concurrent reads while its map remains unchanged.
	// Hide its write methods because cache workers can read at any time.
	source := struct{ eris.BlockSource }{blocks}
	cached, err := cache.New(source, cache.Config{
		CapacityBytes: 16 << 20,
		Workers:       4,
		QueueSize:     64,
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = cached.Close() }()

	ctx := context.Background()
	eris.PrefetchBlock(ctx, cached, capability.Root.Reference, eris.NoTTL, eris.BlockOptions{HighPriority: true})
	reader, err := eris.NewReader(ctx, capability, cached, eris.BlockOptions{})
	if err != nil {
		panic(err)
	}
	defer func() { _ = reader.Close() }()
	content, err := io.ReadAll(reader)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(content))
	// Output: Hello, cached ERIS!
}

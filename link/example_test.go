package link_test

import (
	"fmt"
	"io"
	"io/fs"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/link"
)

func ExampleNewFS() {
	payload := []byte("hello from block storage")
	capability, blocks, err := eris.EncodeRecommended(payload, eris.Secret{})
	if err != nil {
		panic(err)
	}
	encoded, err := (link.Link{
		Capability: capability, Size: uint64(len(payload)), MIME: "text/plain",
	}).MarshalBinary()
	if err != nil {
		panic(err)
	}
	base := fstest.MapFS{"hello.txt.eris": {Data: encoded}}
	files, err := link.NewFS(base, blocks)
	if err != nil {
		panic(err)
	}
	content, err := fs.ReadFile(files, "hello.txt.eris")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(content))
	// Output: hello from block storage
}

func ExampleFS_Open() {
	payload := []byte("hello")
	capability, blocks, err := eris.EncodeRecommended(payload, eris.Secret{})
	if err != nil {
		panic(err)
	}
	encoded, err := (link.Link{Capability: capability, Size: uint64(len(payload))}).MarshalBinary()
	if err != nil {
		panic(err)
	}
	files, err := link.NewFS(fstest.MapFS{"message": {Data: encoded}}, blocks)
	if err != nil {
		panic(err)
	}
	file, err := files.Open("message")
	if err != nil {
		panic(err)
	}
	defer func() { _ = file.Close() }()

	// Bound the actual read. Stat reports a size from untrusted metadata.
	// One extra byte distinguishes a complete file from a truncated read.
	const maxContentSize = 1024
	content, err := io.ReadAll(io.LimitReader(file, maxContentSize+1))
	if err != nil {
		panic(err)
	}
	if len(content) > maxContentSize {
		fmt.Println("content exceeds the limit")
		return
	}
	fmt.Println(string(content))
	// Output: hello
}

func Example() {
	payload := []byte("hello")
	capability, _, err := eris.EncodeRecommended(payload, eris.Secret{})
	if err != nil {
		panic(err)
	}

	file, err := link.New(capability, uint64(len(payload)), "text/plain")
	if err != nil {
		panic(err)
	}
	filename, err := link.TextValue("greeting.txt")
	if err != nil {
		panic(err)
	}
	file.Attributes = map[string]link.Value{"filename": filename}

	data, err := file.MarshalBinary()
	if err != nil {
		panic(err)
	}
	parsed, err := link.Parse(data)
	if err != nil {
		panic(err)
	}
	fmt.Println(parsed.Size, parsed.MIME, len(parsed.Attributes))
	// Output: 5 text/plain 1
}

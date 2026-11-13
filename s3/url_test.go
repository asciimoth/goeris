package s3

import (
	"errors"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestStoreURL(t *testing.T) {
	t.Parallel()
	const key = "FGIBBZ22V4GRMJVXNCSLVQHRP3NAPHRR5NW3GDPPXRJKBI452YBA"
	for _, text := range []string{
		"s3://s3.example/blocks/",
		"s3://localhost:9000/eris/",
		"s3://[::1]:9000/eris/",
		"s3://[2001:db8::1]/eris/",
		"s3://s3.eu-central-1.wasabisys.com/eris/",
		"s3://s3.amazonaws.com/my-block-bucket/",
	} {
		store, err := ParseStoreURL(text)
		if err != nil || store.String() != text || store.Host() == "" || store.Bucket() == "" {
			t.Errorf("ParseStoreURL(%q) = %#v, %v", text, store, err)
			continue
		}
		object, err := store.ObjectURL(key)
		if err != nil || object.Scheme != "https" || object.Host != store.Host() || object.Path != "/"+store.Bucket()+"/"+key {
			t.Errorf("ObjectURL() = %v, %v", object, err)
		}
	}
}

func TestEERLayoutExamples(t *testing.T) {
	t.Parallel()
	keys := []string{
		"FGIBBZ22V4GRMJVXNCSLVQHRP3NAPHRR5NW3GDPPXRJKBI452YBA",
		"RY2DQNYEPJD5TOJZME7MEK3RFFF75ZOIIFAJTKHP4SEMI3CPJXAQ",
		"EHQ2P3JZUX6TP3PZKFPMWOK4SBQKOEWKU52YJJQTKQJ66WWEQX4Q",
		"CMC7JNIYGVRPDTU4M2OUPKFNEEUPXYDWUNSINMAOBJXYUZA32CBA",
	}
	store, err := ParseStoreURL("s3://s3.amazonaws.com/my-block-bucket/")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		ref, err := eris.ParseReference(key)
		if err != nil || ref.String() != key {
			t.Errorf("reference %q = %q, %v", key, ref.String(), err)
			continue
		}
		object, err := store.ObjectURL(ref.String())
		want := "https://s3.amazonaws.com/my-block-bucket/" + key
		if err != nil || object.String() != want {
			t.Errorf("object URL = %v, %v; want %s", object, err, want)
		}
	}
}

func TestStoreURLErrors(t *testing.T) {
	t.Parallel()
	invalid := []string{
		"", "https://s3.example/eris/", "s3:///eris/", "s3://user@s3.example/eris/",
		"s3://s3.example/", "s3://s3.example/eris", "s3://s3.example/a/b/",
		"s3://s3.example/./", "s3://s3.example/a%2Fb/", "s3://s3.example/eris/?x=1",
		"s3://s3.example/eris/#x", "s3://s3.example:/eris/", "s3://s3.example:0/eris/",
		"s3://s3.example:65536/eris/", "s3://s3.example/bad bucket/", "s3://s3.example/bücket/",
		"s3://s3.example:1:2/eris/", "s3://bücher.example/eris/",
		"s3://example\"evil/eris/", "s3://example;evil/eris/", "s3://example]evil/eris/",
		"S3://s3.example/eris/",
		"s3://[example]/eris/", "s3://[127.0.0.1]/eris/",
		"s3://[:::1]/eris/", "s3://[fe80::1%25eth0]/eris/",
	}
	for _, text := range invalid {
		if store, err := ParseStoreURL(text); err == nil || store.String() != "" {
			t.Errorf("ParseStoreURL(%q) = %#v, %v", text, store, err)
		}
	}
	store, _ := ParseStoreURL("s3://s3.example/eris/")
	for _, key := range []string{"", "KEY", "a/b", "a\\b", strings.Repeat("a", 52)} {
		if _, err := store.ObjectURL(key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("ObjectURL(%q) = %v", key, err)
		}
	}
	const validKey = "FGIBBZ22V4GRMJVXNCSLVQHRP3NAPHRR5NW3GDPPXRJKBI452YBA"
	if _, err := (StoreURL{}).ObjectURL(validKey); !errors.Is(err, ErrInvalidStoreURL) {
		t.Fatalf("zero StoreURL = %v", err)
	}
}

func FuzzParseStoreURL(f *testing.F) {
	for _, seed := range []string{"s3://s3.example/eris/", "", "s3://[::1]:9000/a/"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		store, err := ParseStoreURL(text)
		if err != nil {
			return
		}
		if store.String() != text {
			t.Fatalf("non-canonical success: %q != %q", store.String(), text)
		}
		reparsed, err := ParseStoreURL(store.String())
		if err != nil || reparsed != store {
			t.Fatalf("round trip = %#v, %v", reparsed, err)
		}
	})
}

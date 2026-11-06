package dns

import (
	"reflect"
	"testing"
)

func FuzzParse(f *testing.F) {
	link, _ := New(NamespaceERIS, testCapability())
	f.Add(link.String())
	f.Add("dnslink=/eris/bad")
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		parsed, err := Parse(text)
		if err != nil {
			return
		}
		if parsed.String() != text {
			t.Fatalf("accepted non-canonical text %q as %q", text, parsed.String())
		}
		reparsed, err := Parse(parsed.String())
		if err != nil || reparsed != parsed {
			t.Fatalf("round trip = %#v, %v", reparsed, err)
		}
	})
}

func FuzzExtract(f *testing.F) {
	link, _ := New(NamespaceERIS, testCapability())
	f.Add("other=value " + link.String())
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		links := Extract(text)
		for index, extracted := range links {
			if _, err := Parse(extracted.String()); err != nil {
				t.Fatalf("Extract returned invalid link: %v", err)
			}
			if index > 0 && links[index-1].String() > extracted.String() {
				t.Fatal("Extract result is not sorted")
			}
		}
	})
}

func FuzzUpdate(f *testing.F) {
	link, _ := New(NamespaceERIS, testCapability())
	f.Add("")
	f.Add("v=spf1 dnslink=/eris/bad")
	f.Add(link.String() + " " + link.String())
	f.Fuzz(func(t *testing.T, text string) {
		updated, err := Update(text, link)
		if err != nil {
			t.Fatal(err)
		}
		links := Extract(updated)
		count := 0
		for _, extracted := range links {
			if extracted.Namespace == link.Namespace {
				count++
				if !reflect.DeepEqual(extracted, link) {
					t.Fatalf("Update kept a different link: %#v", extracted)
				}
			}
		}
		if count != 1 {
			t.Fatalf("Update produced %d links for namespace", count)
		}
		again, err := Update(updated, link)
		if err != nil || again != updated {
			t.Fatalf("Update is not idempotent: %q, %v", again, err)
		}
	})
}

func FuzzUpdateRecords(f *testing.F) {
	link, _ := New(NamespaceERISFS, testCapability())
	f.Add("", "v=spf1", link.String())
	f.Add(link.String(), "dnslink=/erisfs/bad", "")
	f.Fuzz(func(t *testing.T, first, second, third string) {
		records := []string{first, second, third}
		original := append([]string(nil), records...)
		updated, err := UpdateRecords(records, link)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(records, original) {
			t.Fatal("UpdateRecords changed its input")
		}
		count := 0
		for _, extracted := range Extract(updated...) {
			if extracted.Namespace == link.Namespace {
				count++
				if extracted != link {
					t.Fatalf("UpdateRecords kept a different link: %#v", extracted)
				}
			}
		}
		if count != 1 {
			t.Fatalf("UpdateRecords produced %d links for namespace", count)
		}
		again, err := UpdateRecords(updated, link)
		if err != nil || !reflect.DeepEqual(again, updated) {
			t.Fatalf("UpdateRecords is not idempotent: %#v, %v", again, err)
		}
	})
}

func FuzzOwnerName(f *testing.F) {
	f.Add("example.org")
	f.Add("_dnslink.example.org.")
	f.Add("")
	f.Fuzz(func(t *testing.T, name string) {
		owner, err := OwnerName(name)
		if err != nil {
			return
		}
		if owner == "" {
			t.Fatal("valid owner is empty")
		}
		again, err := OwnerName(owner)
		if err != nil || again != owner {
			t.Fatalf("OwnerName is not idempotent: %q, %v", again, err)
		}
	})
}

package url

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	eris "github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/fetch"
)

type recordingLocks struct {
	mu          sync.Mutex
	entries     map[[32]byte]string
	reads       [][32]byte
	writes      [][32]byte
	afterLookup func()
	afterAdd    func()
}

func (db *recordingLocks) Lookup(_ context.Context, hash [32]byte) string {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.reads = append(db.reads, hash)
	if db.afterLookup != nil {
		db.afterLookup()
	}
	return db.entries[hash]
}

func (db *recordingLocks) Add(_ context.Context, hash [32]byte, urn string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.writes = append(db.writes, hash)
	if db.entries == nil {
		db.entries = make(map[[32]byte]string)
	}
	if _, exists := db.entries[hash]; !exists {
		db.entries[hash] = urn
	}
	if db.afterAdd != nil {
		db.afterAdd()
	}
}

func TestURLLockVariants(t *testing.T) {
	const mirrors = "alt https://b.test/data\nalt https://a.test/data\nalt https://c.test/%41\nalt https://b.test/data\n"
	const name = "name  file #1 \n"
	const treeDefaults = "erisfs.use true\nerisfs.layout flat\nerisfs.version 2\n"
	secret := "eris.secret " + strings.Repeat("01", 32) + "\n"
	zero := "eris.secret " + strings.Repeat("0", 64) + "\n"
	for _, tc := range []struct{ label, fields, keep, kind string }{
		{"blob with empty secret", "eris.secret\nerisfs.use false\n", "", "blob"},
		{"blob with zero secret", zero, "", "blob"},
		{"private blob", "erisfs.use false\n" + secret, secret, "blob"},
		{"tree defaults", treeDefaults + name + zero + "executable false\nerisfs.compression none\n", name, "erisfs"},
		{"tree options", treeDefaults + name + secret + "erisfs.compression auto\nexecutable true\n", name + secret + "erisfs.compression auto\nexecutable true\n", "erisfs"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			var db recordingLocks
			var hosts []string
			f, err := New(Config{
				Client: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
					if len(db.writes) != 0 {
						t.Fatal("locks written before a successful fetch")
					}
					hosts = append(hosts, r.URL.Host)
					if r.URL.Host == "a.test" {
						return response(200, "wrong"), nil
					}
					return response(200, "Hello world!"), nil
				})},
				Store: eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)},
				Locks: &db,
			})
			if err != nil {
				t.Fatal(err)
			}
			full := source(t, description+mirrors+checksum+tc.fields)
			selected := source(t, "type url\nurl https://b.test/data\n"+tc.keep)
			wantReads := [][32]byte{full.Hash()}
			wantWrites := [][32]byte{full.Hash(), selected.Hash()}
			got := f.Fetch(t.Context(), full)
			if got.Status != "ok" || got.Kind != tc.kind || !slices.Equal(hosts, []string{"a.test", "b.test"}) {
				t.Fatalf("fetch returned %+v after requests to %v", got, hosts)
			}
			if !slices.Equal(db.reads, wantReads) || !slices.Equal(db.writes, wantWrites) {
				t.Fatalf("reads %x, want %x, writes %x, want %x", db.reads, wantReads, db.writes, wantWrites)
			}
			for _, s := range []fetch.Source{full, selected} {
				if urn := db.entries[s.Hash()]; urn != got.Cap.String() {
					t.Errorf("stored URN %q, want %q", urn, got.Cap)
				}
				hit := f.Fetch(t.Context(), s)
				if hit.Status != "ok" || hit.Kind != got.Kind || hit.Cap != got.Cap {
					t.Fatalf("locked fetch returned %+v, want %+v", hit, got)
				}
			}
			if len(hosts) != 2 || len(db.writes) != len(wantWrites) {
				t.Fatal("lock hit downloaded or wrote new locks")
			}
			unchecked := source(t, description+mirrors+tc.fields)
			hit := f.Fetch(t.Context(), unchecked)
			wantWrites = append(wantWrites, unchecked.Hash())
			if hit.Status != "ok" || hit.Kind != got.Kind || hit.Cap != got.Cap || len(hosts) != 2 ||
				!slices.Equal(db.writes, wantWrites) || db.entries[unchecked.Hash()] != got.Cap.String() {
				t.Fatalf("minified hit returned %+v, requests %v, writes %x", hit, hosts, db.writes)
			}
		})
	}
}

func TestURLLockLookupOrder(t *testing.T) {
	full := source(t, description+"alt https://b.test/\nalt https://a.test/data\nalt https://b.test/\nalt https://c.test/\n")
	keys := [][32]byte{full.Hash(), source(t, description).Hash(),
		source(t, "type url\nurl https://b.test/").Hash(), source(t, "type url\nurl https://c.test/").Hash()}
	for i := range keys {
		db := recordingLocks{entries: make(map[[32]byte]string)}
		var want eris.Capability
		for j, key := range keys {
			if j < i {
				db.entries[key] = "invalid URN"
				continue
			}
			capability := eris.Capability{BlockSize: eris.BlockSize1KiB, Root: eris.Pair{Reference: eris.Reference{byte(j)}}}
			db.entries[key] = capability.String()
			if j == i {
				want = capability
			}
		}
		f, err := New(Config{
			Client: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
				t.Fatal("download on lock hit")
				return nil, nil
			})},
			Store:    brokenStore{},
			Locks:    &db,
			Progress: func(ProgressNode) { t.Fatal("download progress on lock hit") },
		})
		if err != nil {
			t.Fatal(err)
		}
		hit := f.Fetch(t.Context(), full)
		var writes [][32]byte
		if i > 0 {
			writes = keys[:1]
		}
		if hit.Status != "ok" || hit.Kind != "blob" || hit.Cap != want || !slices.Equal(db.writes, writes) || !slices.Equal(db.reads, keys[:i+1]) {
			t.Fatalf("hit %d returned %+v, reads %x, writes %x", i, hit, db.reads, db.writes)
		}
	}
}

func TestURLAliasHitBindsExactRequest(t *testing.T) {
	var db recordingLocks
	var hosts []string
	f := setupWithLocks(t, func(r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Host)
		return response(200, r.URL.Host), nil
	}, &db)
	primary := source(t, description)
	mirror := source(t, "type url\nurl https://b.test/\n")
	combined := source(t, description+"alt https://b.test/\n")
	first := f.Fetch(t.Context(), mirror)
	hit := f.Fetch(t.Context(), combined)
	newPrimary := f.Fetch(t.Context(), primary)
	if first.Status != "ok" || hit.Status != "ok" || newPrimary.Status != "ok" || hit.Cap != first.Cap || newPrimary.Cap == first.Cap {
		t.Fatalf("mirror returned %+v, alias hit %+v, new primary %+v", first, hit, newPrimary)
	}
	if got := f.Fetch(t.Context(), combined); got.Status != "ok" || got.Cap != first.Cap || !slices.Equal(hosts, []string{"b.test", "a.test"}) {
		t.Fatalf("repeated request returned %+v, want %+v, requests %v", got, first, hosts)
	}
}

func TestURLMirrorLockAtLineLimit(t *testing.T) {
	const base = "https://b.test/"
	mirror := base + strings.Repeat("a", fetch.MaxLineSize-len("alt "+base))
	name := "name " + strings.Repeat("n", fetch.MaxLineSize-len("name ")) + "\n"
	src := source(t, description+"alt "+mirror+"\n"+name+"erisfs.version 2\n")
	primary := source(t, description+name)
	selected := source(t, "type url\nurl "+mirror+"\n"+name)
	capability := eris.Capability{BlockSize: eris.BlockSize1KiB}
	db := recordingLocks{entries: map[[32]byte]string{selected.Hash(): capability.String()}}
	f := setupWithLocks(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("download on mirror lock hit")
		return nil, nil
	}, &db)
	got := f.Fetch(t.Context(), src)
	if got.Status != "ok" || got.Kind != "erisfs" || got.Cap != capability {
		t.Fatalf("mirror hit returned %+v", got)
	}
	if want := [][32]byte{src.Hash(), primary.Hash(), selected.Hash()}; !slices.Equal(db.reads, want) {
		t.Fatalf("lock reads %x, want %x", db.reads, want)
	}
	if want := [][32]byte{src.Hash()}; !slices.Equal(db.writes, want) || db.entries[src.Hash()] != capability.String() {
		t.Fatalf("lock writes %x, want %x", db.writes, want)
	}
}

func TestURLChecksumRequiresExactLock(t *testing.T) {
	var db recordingLocks
	var calls int
	f := setupWithLocks(t, func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, "wrong"), nil
	}, &db)
	unchecked := source(t, description)
	if got := f.Fetch(t.Context(), unchecked); got.Status != "ok" || calls != 1 {
		t.Fatalf("seed fetch returned %+v after %d requests", got, calls)
	}
	checked := source(t, description+checksum)
	got := f.Fetch(t.Context(), checked)
	if got.Status != "fail" || got.Failure.Code != "integrity" || calls != 2 {
		t.Fatalf("checked fetch returned %+v after %d requests", got, calls)
	}
	if want := [][32]byte{unchecked.Hash(), checked.Hash()}; !slices.Equal(db.reads, want) || len(db.writes) != 1 {
		t.Fatalf("lock reads %x, want %x, writes %x", db.reads, want, db.writes)
	}
}

func TestURLConcurrentMissesPreserveFirstLock(t *testing.T) {
	var db recordingLocks
	started := make(chan struct{})
	release := make(chan struct{})
	slow := setupWithLocks(t, func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return response(200, "later"), nil
	}, &db)
	fast := setupWithLocks(t, func(*http.Request) (*http.Response, error) {
		return response(200, "first"), nil
	}, &db)
	src := source(t, description)
	done := make(chan fetch.Response, 1)
	go func() { done <- slow.Fetch(t.Context(), src) }()
	select {
	case <-started:
	case early := <-done:
		t.Fatalf("slow fetch returned before download: %+v", early)
	}
	first := fast.Fetch(t.Context(), src)
	close(release)
	later := <-done
	if first.Status != "ok" || later.Status != "ok" || first.Cap == later.Cap {
		t.Fatalf("first fetch returned %+v, later fetch returned %+v", first, later)
	}
	if got := fast.Fetch(t.Context(), src); got.Status != "ok" || got.Cap != first.Cap || len(db.writes) != 2 {
		t.Fatalf("lock hit returned %+v, want %+v, writes %x", got, first, db.writes)
	}
}

func TestURLFailedFetchDoesNotLock(t *testing.T) {
	for _, code := range []string{"invalid-source", "not-found", "integrity", "store", "internal", "cancelled"} {
		t.Run(code, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var db recordingLocks
			f := setupWithLocks(t, func(*http.Request) (*http.Response, error) {
				switch code {
				case "invalid-source":
					t.Fatal("download before validation")
				case "not-found":
					return response(404, ""), nil
				case "integrity":
					return response(200, "wrong"), nil
				}
				return response(200, "Hello world!"), nil
			}, &db)
			fields := checksum
			switch code {
			case "invalid-source":
				fields += "eris.secret invalid\n"
			case "store":
				f.config.Store = brokenStore{}
			case "internal":
				// Cleanup can fail after all blocks have been written.
				f.config.TempStorage = tempStorageFunc(func(context.Context) (fetch.TempData, error) {
					return &testTempData{closeErr: errors.New("close failed")}, nil
				})
			case "cancelled":
				f.config.Store = cancellingStore{cancel: cancel}
			}
			got := f.Fetch(ctx, source(t, description+fields))
			if got.Status != "fail" || got.Failure.Code != code || len(db.writes) != 0 {
				t.Fatalf("got %+v, writes %x", got, db.writes)
			}
			if code == "invalid-source" && len(db.reads) != 0 {
				t.Fatalf("lock lookups before validation: %x", db.reads)
			}
		})
	}
}

func TestURLCancellationDuringLockIO(t *testing.T) {
	src := source(t, description+"alt https://b.test/\n")
	t.Run("lookup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		capability := eris.Capability{BlockSize: eris.BlockSize1KiB}
		db := recordingLocks{
			entries:     map[[32]byte]string{src.Hash(): capability.String()},
			afterLookup: cancel,
		}
		f := setupWithLocks(t, func(*http.Request) (*http.Response, error) {
			t.Fatal("download after cancellation")
			return nil, nil
		}, &db)
		got := f.Fetch(ctx, src)
		if got.Status != "fail" || got.Failure.Code != "cancelled" || len(db.reads) != 1 || len(db.writes) != 0 {
			t.Fatalf("response %+v, reads %x, writes %x", got, db.reads, db.writes)
		}
	})
	t.Run("write after minified hit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		primary := source(t, description)
		capability := eris.Capability{BlockSize: eris.BlockSize1KiB}
		db := recordingLocks{
			entries:  map[[32]byte]string{primary.Hash(): capability.String()},
			afterAdd: cancel,
		}
		f := setupWithLocks(t, func(*http.Request) (*http.Response, error) {
			t.Fatal("download after lock hit")
			return nil, nil
		}, &db)
		got := f.Fetch(ctx, src)
		if got.Status != "ok" || got.Cap != capability || !slices.Equal(db.writes, [][32]byte{src.Hash()}) || db.entries[src.Hash()] != capability.String() {
			t.Fatalf("response %+v, writes %x", got, db.writes)
		}
	})
	t.Run("write after success", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		db := recordingLocks{afterAdd: cancel}
		f := setupWithLocks(t, func(*http.Request) (*http.Response, error) {
			return response(200, "Hello world!"), nil
		}, &db)
		got := f.Fetch(ctx, src)
		if got.Status != "ok" || !slices.Equal(db.writes, [][32]byte{src.Hash()}) || db.entries[src.Hash()] != got.Cap.String() {
			t.Fatalf("response %+v, writes %x", got, db.writes)
		}
	})
}

func TestURLNilLocksFetchAgain(t *testing.T) {
	calls := 0
	f := setup(t, func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, strings.Repeat("a", calls)), nil
	})
	s := source(t, description)
	first, second := f.Fetch(t.Context(), s), f.Fetch(t.Context(), s)
	if calls != 2 || first.Status != "ok" || second.Status != "ok" || first.Cap == second.Cap {
		t.Fatalf("got %+v and %+v after %d requests", first, second, calls)
	}
}

func TestURLRetryAfterChecksumFailure(t *testing.T) {
	calls := 0
	checked := source(t, description+checksum)
	var db recordingLocks
	f := setupWithLocks(t, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(200, "wrong"), nil
		}
		return response(200, "Hello world!"), nil
	}, &db)
	if got := f.Fetch(t.Context(), checked); got.Failure.Code != "integrity" || len(db.writes) != 0 {
		t.Fatalf("first fetch returned %+v, writes %x", got, db.writes)
	}
	retry := f.Fetch(t.Context(), checked)
	exact := f.Fetch(t.Context(), checked)
	alias := f.Fetch(t.Context(), source(t, description))
	if retry.Status != "ok" || exact.Status != "ok" || alias.Status != "ok" || exact.Cap != retry.Cap || alias.Cap != retry.Cap || calls != 2 {
		t.Fatalf("retry returned %+v, exact hit %+v, alias hit %+v, requests %d", retry, exact, alias, calls)
	}
}

func TestURLDistinctEncodingLocks(t *testing.T) {
	calls := 0
	var db recordingLocks
	f := setupWithLocks(t, func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, "Hello world!"), nil
	}, &db)
	for i, desc := range []string{
		description,
		description + "name file",
		description + "name other",
		description + "name file\nexecutable true",
		description + "name file\nerisfs.compression auto",
		description + "eris.secret " + strings.Repeat("ab", 32),
		"type url\nurl https://a.test/%64ata",
	} {
		s := source(t, desc)
		got := f.Fetch(t.Context(), s)
		if got.Status != "ok" || calls != i+1 {
			t.Fatalf("source %d returned %+v after %d requests", i, got, calls)
		}
		// These descriptions already have their minified form.
		if len(db.writes) != i+1 || db.writes[i] != s.Hash() {
			t.Fatalf("source %d wrote unexpected keys %x", i, db.writes)
		}
	}
}

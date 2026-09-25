// Package storagetest checks that a storage.ObjectStore behaves the way the
// sync engine relies on. The same suite runs against the in-memory store,
// against local fakes of each provider's protocol and, on demand, against a
// real bucket.
package storagetest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/storage"
)

// Run runs the suite against s. Every key starts with prefix, so the suite
// can run inside a real bucket; everything it creates is deleted at the end.
func Run(t *testing.T, s storage.ObjectStore, prefix string) {
	ctx := context.Background()
	t.Cleanup(func() { deletePrefix(t, s, prefix) })

	t.Run("MissingObject", func(t *testing.T) {
		key := prefix + "missing/none"
		if _, _, err := s.Get(ctx, key); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Get of a missing object: %v, want ErrNotFound", err)
		}
		if _, err := s.Head(ctx, key); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Head of a missing object: %v, want ErrNotFound", err)
		}
		if _, err := s.Put(ctx, key, []byte("x"), storage.Precondition{IfMatch: `"0123"`}); !errors.Is(err, storage.ErrPreconditionFailed) {
			t.Errorf("IfMatch write to a missing object: %v, want ErrPreconditionFailed", err)
		}
		if err := s.Delete(ctx, key); err != nil {
			t.Errorf("Delete of a missing object: %v, want no error", err)
		}
	})

	t.Run("CreateGuard", func(t *testing.T) {
		key := prefix + "create/obj"
		mustPut(t, s, key, "v1", storage.Precondition{IfNoneMatch: true})
		if _, err := s.Put(ctx, key, []byte("v2"), storage.Precondition{IfNoneMatch: true}); !errors.Is(err, storage.ErrPreconditionFailed) {
			t.Fatalf("IfNoneMatch write over an existing object: %v, want ErrPreconditionFailed", err)
		}
		assertContent(t, s, key, "v1")
	})

	t.Run("MatchGuard", func(t *testing.T) {
		key := prefix + "match/obj"
		first := etagOf(t, s, key, mustPut(t, s, key, "v1", storage.Precondition{}))
		second := etagOf(t, s, key, mustPut(t, s, key, "v2", storage.Precondition{IfMatch: first}))
		if second == first {
			t.Errorf("ETag did not change after a write: %s", first)
		}
		if _, err := s.Put(ctx, key, []byte("v3"), storage.Precondition{IfMatch: first}); !errors.Is(err, storage.ErrPreconditionFailed) {
			t.Fatalf("write with a stale ETag: %v, want ErrPreconditionFailed", err)
		}
		assertContent(t, s, key, "v2")
	})

	t.Run("ETagsAgree", func(t *testing.T) {
		key := prefix + "etag/obj"
		etag := etagOf(t, s, key, mustPut(t, s, key, "content", storage.Precondition{}))
		if _, got, err := s.Get(ctx, key); err != nil || got != etag {
			t.Errorf("Get ETag = %q, %v; want %q", got, err, etag)
		}
		listed := listKeys(t, s, prefix+"etag/")
		if len(listed) != 1 || listed[0].ETag != etag {
			t.Errorf("List = %+v, want one entry with ETag %q", listed, etag)
		}
	})

	t.Run("UnconditionalOverwrite", func(t *testing.T) {
		key := prefix + "overwrite/obj"
		mustPut(t, s, key, "v1", storage.Precondition{})
		mustPut(t, s, key, "v2", storage.Precondition{})
		assertContent(t, s, key, "v2")
	})

	t.Run("ConcurrentCreate", func(t *testing.T) {
		key := prefix + "race/obj"
		const writers = 8
		errs := make([]error, writers)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = s.Put(ctx, key, []byte(fmt.Sprintf("writer-%d", i)), storage.Precondition{IfNoneMatch: true})
			}(i)
		}
		wg.Wait()
		wins := 0
		for _, err := range errs {
			switch {
			case err == nil:
				wins++
			case !errors.Is(err, storage.ErrPreconditionFailed):
				t.Errorf("losing writer got %v, want ErrPreconditionFailed", err)
			}
		}
		if wins != 1 {
			t.Errorf("%d concurrent creates succeeded, want exactly 1", wins)
		}
	})

	t.Run("ListPrefixAndNames", func(t *testing.T) {
		names := []string{"list/a", "list/b/c", "list/sp ace/ş ç.md", "list/sym+b%ol#s.md"}
		for _, n := range names {
			mustPut(t, s, prefix+n, n, storage.Precondition{})
		}
		mustPut(t, s, prefix+"listx", "sibling", storage.Precondition{})

		var got []string
		for _, o := range listKeys(t, s, prefix+"list/") {
			got = append(got, o.Key[len(prefix):])
		}
		sort.Strings(got)
		want := append([]string(nil), names...)
		sort.Strings(want)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("List(list/) = %q, want %q", got, want)
		}
		for _, n := range names {
			assertContent(t, s, prefix+n, n)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		key := prefix + "delete/obj"
		mustPut(t, s, key, "v1", storage.Precondition{})
		if err := s.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Get(ctx, key); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Get after Delete: %v, want ErrNotFound", err)
		}
		if got := listKeys(t, s, prefix+"delete/"); len(got) != 0 {
			t.Errorf("List after Delete = %+v, want empty", got)
		}
	})

	t.Run("Probe", func(t *testing.T) {
		r, err := storage.Probe(ctx, s, prefix+"probe/")
		if err != nil {
			t.Fatal(err)
		}
		if !r.ConditionalWrites() {
			t.Errorf("Probe = %+v, want conditional writes", r)
		}
		if got := listKeys(t, s, prefix+"probe/"); len(got) != 0 {
			t.Errorf("Probe left objects behind: %+v", got)
		}
	})
}

func mustPut(t *testing.T, s storage.ObjectStore, key, data string, pre storage.Precondition) string {
	t.Helper()
	etag, err := s.Put(context.Background(), key, []byte(data), pre)
	if err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
	return etag
}

// etagOf returns the ETag a Put reported or, for backends that report none,
// the one Head returns.
func etagOf(t *testing.T, s storage.ObjectStore, key, reported string) string {
	t.Helper()
	info, err := s.Head(context.Background(), key)
	if err != nil {
		t.Fatalf("Head(%q): %v", key, err)
	}
	if reported != "" && info.ETag != reported {
		t.Fatalf("Head ETag %q differs from the ETag Put reported, %q", info.ETag, reported)
	}
	return info.ETag
}

func assertContent(t *testing.T, s storage.ObjectStore, key, want string) {
	t.Helper()
	data, _, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	if string(data) != want {
		t.Errorf("content of %q = %q, want %q", key, data, want)
	}
}

func listKeys(t *testing.T, s storage.ObjectStore, prefix string) []storage.ObjectInfo {
	t.Helper()
	objs, err := s.List(context.Background(), prefix)
	if err != nil {
		t.Fatalf("List(%q): %v", prefix, err)
	}
	return objs
}

func deletePrefix(t *testing.T, s storage.ObjectStore, prefix string) {
	ctx := context.Background()
	objs, err := s.List(ctx, prefix)
	if err != nil {
		t.Errorf("cleanup: List(%q): %v", prefix, err)
		return
	}
	for _, o := range objs {
		if err := s.Delete(ctx, o.Key); err != nil {
			t.Errorf("cleanup: Delete(%q): %v", o.Key, err)
		}
	}
}

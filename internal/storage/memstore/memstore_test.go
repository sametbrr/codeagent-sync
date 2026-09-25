package memstore

import (
	"context"
	"errors"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/storage"
	"github.com/sametbrr/codeagent-sync/internal/storage/storagetest"
)

func TestSuite(t *testing.T) {
	storagetest.Run(t, New(), "suite/")
}

// BeforePut lets a test slip in a competing write between a read and the
// conditional write that depends on it.
func TestBeforePutSimulatesACompetingWriter(t *testing.T) {
	ctx := context.Background()
	s := New()
	etag, err := s.Put(ctx, "k", []byte("base"), storage.Precondition{})
	if err != nil {
		t.Fatal(err)
	}

	s.BeforePut = func(key string) {
		s.BeforePut = nil
		if _, err := s.Put(ctx, key, []byte("other machine"), storage.Precondition{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(ctx, "k", []byte("mine"), storage.Precondition{IfMatch: etag}); !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("write after a competing write: %v, want ErrPreconditionFailed", err)
	}
	data, _, _ := s.Get(ctx, "k")
	if string(data) != "other machine" {
		t.Errorf("content = %q, want the competing write", data)
	}
}

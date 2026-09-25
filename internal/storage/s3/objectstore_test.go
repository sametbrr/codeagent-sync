package s3

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sametbrr/codeagent-sync/internal/storage"
	"github.com/sametbrr/codeagent-sync/internal/storage/s3/s3fake"
	"github.com/sametbrr/codeagent-sync/internal/storage/storagetest"
)

func newFakeClient(t *testing.T) (storage.ObjectStore, *s3fake.Server) {
	t.Helper()
	fake := s3fake.New("test-bucket")
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	s, err := New(&storage.StorageConfig{
		Provider:        storage.ProviderS3,
		Bucket:          "test-bucket",
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "secret",
		Region:          "us-east-1",
		Endpoint:        srv.URL,
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, fake
}

// The suite runs the real SDK client against the fake server, so it checks
// that the conditional headers are sent and S3's answers are mapped.
func TestSuiteAgainstFakeS3(t *testing.T) {
	s, _ := newFakeClient(t)
	storagetest.Run(t, s, "suite/")
}

func TestConditionalConflictIsAPreconditionFailure(t *testing.T) {
	s, fake := newFakeClient(t)
	fake.Force("racy", http.StatusConflict, "ConditionalRequestConflict")

	_, err := s.Put(context.Background(), "racy", []byte("x"), storage.Precondition{IfNoneMatch: true})
	if !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Errorf("409 ConditionalRequestConflict: %v, want ErrPreconditionFailed", err)
	}
}

func TestMissingBucketIsNotAMissingObject(t *testing.T) {
	s, fake := newFakeClient(t)
	fake.Force("key", http.StatusNotFound, "NoSuchBucket")

	_, _, err := s.Get(context.Background(), "key")
	if err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Errorf("NoSuchBucket: %v, want an error that is not ErrNotFound", err)
	}
}

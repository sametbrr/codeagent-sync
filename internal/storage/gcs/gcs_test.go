package gcs

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	appstorage "github.com/sametbrr/codeagent-sync/internal/storage"
	"github.com/sametbrr/codeagent-sync/internal/storage/storagetest"
)

// The suite runs the real GCS client library against the fake server, which
// it reaches through STORAGE_EMULATOR_HOST.
func TestSuiteAgainstFakeGCS(t *testing.T) {
	srv := httptest.NewServer(newFakeGCS("test-bucket"))
	t.Cleanup(srv.Close)
	t.Setenv("STORAGE_EMULATOR_HOST", strings.TrimPrefix(srv.URL, "http://"))

	s, err := New(&appstorage.StorageConfig{
		Provider:  appstorage.ProviderGCS,
		Bucket:    "test-bucket",
		ProjectID: "test-project",
	})
	if err != nil {
		t.Fatal(err)
	}
	storagetest.Run(t, s, "suite/")
}

func TestConditionsFor(t *testing.T) {
	cond, err := conditionsFor(appstorage.Precondition{})
	if cond != nil || err != nil {
		t.Errorf("no precondition = %+v, %v; want none", cond, err)
	}

	cond, err = conditionsFor(appstorage.Precondition{IfNoneMatch: true})
	if err != nil || cond == nil || !cond.DoesNotExist {
		t.Errorf("IfNoneMatch = %+v, %v; want DoesNotExist", cond, err)
	}

	cond, err = conditionsFor(appstorage.Precondition{IfMatch: "1234"})
	if err != nil || cond == nil || cond.GenerationMatch != 1234 {
		t.Errorf("IfMatch 1234 = %+v, %v; want GenerationMatch 1234", cond, err)
	}

	// An ETag from another provider can never match a GCS generation.
	for _, bad := range []string{`"d41d8cd98f00b204e9800998ecf8427e"`, "0", "-5"} {
		if _, err := conditionsFor(appstorage.Precondition{IfMatch: bad}); !errors.Is(err, appstorage.ErrPreconditionFailed) {
			t.Errorf("IfMatch %q: %v, want ErrPreconditionFailed", bad, err)
		}
	}
}

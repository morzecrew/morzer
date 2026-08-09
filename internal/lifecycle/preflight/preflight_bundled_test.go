package preflight

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/events"
)

const bundledSHA = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

// The check that stands between a mutable tag and a digest-pinned deployment.
// Its ordinary paths are covered by the suite; what follows are the branches
// that only run when something is already wrong, which is exactly the code
// that must not be dead.

func TestBundledImagesRefusesWhenTheStoreCannotBeAsked(t *testing.T) {
	check := BundledImages([]string{"registry.example/demo/app@" + bundledSHA},
		func(context.Context, string) (bool, error) {
			return false, errors.New("cannot connect to the Docker daemon")
		})

	got := check.Run(context.Background())
	if got.Status != events.CheckFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
	// "Cannot tell" must not be reported as "not here": one sends an
	// operator to start their runtime and the other to run an ingest.
	if strings.Contains(got.Message, "not in the local image store") {
		t.Errorf("a daemon that could not answer was reported as a missing image: %s",
			got.Message)
	}
	if !strings.Contains(got.Remedy, "runtime is running") {
		t.Errorf("the remedy does not name the runtime: %q", got.Remedy)
	}
}

func TestBundledImagesRefusesAnUnpinnedReference(t *testing.T) {
	check := BundledImages([]string{"registry.example/demo/app:latest"},
		func(context.Context, string) (bool, error) { return true, nil })

	got := check.Run(context.Background())
	if got.Status != events.CheckFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
	if !strings.Contains(got.Message, "pinned by digest") {
		t.Errorf("the refusal does not say what is wrong: %s", got.Message)
	}
}

// TestBundledImagesSaysSoWhenTheRuntimeHasNoImageStore.
//
// Not a failure. A runtime with no local store cannot answer, and refusing
// every deployment that bundles an image because the *check* has no way to run
// would be the check deciding something it does not know.
func TestBundledImagesSaysSoWhenTheRuntimeHasNoImageStore(t *testing.T) {
	got := BundledImages([]string{"registry.example/demo/app@" + bundledSHA}, nil).
		Run(context.Background())
	if got.Status != events.CheckOK {
		t.Fatalf("status = %s, want ok", got.Status)
	}
	if !strings.Contains(got.Message, "no local image store") {
		t.Errorf("message = %q", got.Message)
	}
}

func TestBundledImagesIsFatal(t *testing.T) {
	// The property decision 20 rests on. A non-fatal check would be demoted
	// to a warning by the runner, the converge would proceed, and Compose
	// would resolve the missing image from the vendor's registry.
	if !BundledImages(nil, nil).Fatal {
		t.Error("the bundled-image check is advisory, so a missing image would be pulled")
	}
}

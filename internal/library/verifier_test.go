package library

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubSource is a minimal Source used to probe capability detection.
type stubSource struct{ name string }

func (s stubSource) Name() string                                    { return s.name }
func (stubSource) CurrentlyReading(context.Context) ([]Entry, error) { return nil, nil }
func (stubSource) RecentReads(context.Context, int) ([]Entry, error) { return nil, nil }
func (stubSource) ToRead(context.Context) ([]Entry, error)           { return nil, nil }

// verifiableSource is a stubSource that also reports a verification result.
type verifiableSource struct {
	stubSource
	err error
}

func (v verifiableSource) Verify(context.Context) error { return v.err }

func TestAsVerifierDirect(t *testing.T) {
	src := verifiableSource{stubSource: stubSource{name: "v"}, err: errors.New("boom")}
	got, ok := AsVerifier(src)
	if !ok {
		t.Fatal("AsVerifier() ok = false, want a Verifier for a source that implements it")
	}
	if err := got.Verify(context.Background()); err == nil {
		t.Error("Verify() = nil, want the source's error surfaced")
	}
}

func TestAsVerifierSeesThroughCached(t *testing.T) {
	src := verifiableSource{stubSource: stubSource{name: "v"}}
	cached := NewCached(src, time.Minute)

	got, ok := AsVerifier(cached)
	if !ok {
		t.Fatal("AsVerifier(Cached) ok = false, want it to see through the cache")
	}
	if err := got.Verify(context.Background()); err != nil {
		t.Errorf("Verify() = %v, want nil", err)
	}
}

func TestAsVerifierUnsupported(t *testing.T) {
	if _, ok := AsVerifier(stubSource{name: "plain"}); ok {
		t.Error("AsVerifier(plain) ok = true, want false for a source without verification")
	}
}

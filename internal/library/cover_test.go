package library

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// coverSource is a listSource that can also serve cover images.
type coverSource struct {
	listSource
}

func (coverSource) CoverImage(_ context.Context, id string) (io.ReadCloser, string, error) {
	return io.NopCloser(strings.NewReader("img-" + id)), "image/jpeg", nil
}

func TestAsCoverProviderFindsNamedSource(t *testing.T) {
	src := coverSource{listSource{name: "grimmory"}}

	p, ok := AsCoverProvider(src, "grimmory")
	if !ok {
		t.Fatal("AsCoverProvider = false, want the capable source found")
	}
	body, ct, err := p.CoverImage(context.Background(), "7")
	if err != nil {
		t.Fatalf("CoverImage: %v", err)
	}
	defer func() { _ = body.Close() }()
	data, _ := io.ReadAll(body)
	if string(data) != "img-7" || ct != "image/jpeg" {
		t.Errorf("CoverImage = (%q, %q), want (img-7, image/jpeg)", data, ct)
	}
}

func TestAsCoverProviderSeesThroughCache(t *testing.T) {
	cached := NewCached(coverSource{listSource{name: "grimmory"}}, time.Minute)
	if _, ok := AsCoverProvider(cached, "grimmory"); !ok {
		t.Error("AsCoverProvider = false, want capability found through Cached")
	}
}

func TestAsCoverProviderSearchesMulti(t *testing.T) {
	m := Combine(
		listSource{name: "hardcover"},
		NewCached(coverSource{listSource{name: "grimmory"}}, time.Minute),
	)

	if _, ok := AsCoverProvider(m, "grimmory"); !ok {
		t.Error("AsCoverProvider = false, want grimmory found inside Multi")
	}
	if _, ok := AsCoverProvider(m, "hardcover"); ok {
		t.Error("AsCoverProvider = true for hardcover, which has no covers")
	}
	if _, ok := AsCoverProvider(m, "nope"); ok {
		t.Error("AsCoverProvider = true for an unknown source name")
	}
}

func TestAsCoverProviderUnsupported(t *testing.T) {
	if _, ok := AsCoverProvider(listSource{name: "grimmory"}, "grimmory"); ok {
		t.Error("AsCoverProvider = true for a source without the capability")
	}
	if _, ok := AsCoverProvider(nil, "grimmory"); ok {
		t.Error("AsCoverProvider = true for a nil source")
	}
}

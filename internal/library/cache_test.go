package library

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSource records how many times each method is called and lets tests
// control the returned data and errors.
type fakeSource struct {
	reading   int64
	reads     int64
	toRead    int64
	readsErr  error
	toReadErr error
	block     chan struct{} // if non-nil, calls wait on it before returning
}

func (f *fakeSource) Name() string { return "fake" }

func (f *fakeSource) CurrentlyReading(_ context.Context) ([]Entry, error) {
	atomic.AddInt64(&f.reading, 1)
	return []Entry{{Book: Book{Title: "reading"}, Status: StatusCurrentlyRead}}, nil
}

func (f *fakeSource) RecentReads(_ context.Context, limit int) ([]Entry, error) {
	atomic.AddInt64(&f.reads, 1)
	if f.block != nil {
		<-f.block
	}
	if f.readsErr != nil {
		return nil, f.readsErr
	}
	return []Entry{{Book: Book{Title: "read"}, Status: StatusRead}}, nil
}

func (f *fakeSource) ToRead(_ context.Context) ([]Entry, error) {
	atomic.AddInt64(&f.toRead, 1)
	if f.toReadErr != nil {
		return nil, f.toReadErr
	}
	return []Entry{{Book: Book{Title: "tbr"}, Status: StatusWantToRead}}, nil
}

func TestCachedServesWithinTTL(t *testing.T) {
	f := &fakeSource{}
	c := NewCached(f, time.Minute)

	for i := 0; i < 3; i++ {
		if _, err := c.RecentReads(context.Background(), 10); err != nil {
			t.Fatalf("RecentReads: %v", err)
		}
		if _, err := c.ToRead(context.Background()); err != nil {
			t.Fatalf("ToRead: %v", err)
		}
	}

	if got := atomic.LoadInt64(&f.reads); got != 1 {
		t.Errorf("RecentReads hit backend %d times, want 1", got)
	}
	if got := atomic.LoadInt64(&f.toRead); got != 1 {
		t.Errorf("ToRead hit backend %d times, want 1", got)
	}
}

func TestCachedRefetchesAfterTTL(t *testing.T) {
	f := &fakeSource{}
	c := NewCached(f, time.Minute)

	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	if _, err := c.RecentReads(context.Background(), 10); err != nil {
		t.Fatalf("RecentReads: %v", err)
	}
	now = now.Add(2 * time.Minute) // advance past the TTL
	if _, err := c.RecentReads(context.Background(), 10); err != nil {
		t.Fatalf("RecentReads: %v", err)
	}

	if got := atomic.LoadInt64(&f.reads); got != 2 {
		t.Errorf("RecentReads hit backend %d times, want 2", got)
	}
}

func TestCachedDoesNotCacheErrors(t *testing.T) {
	f := &fakeSource{readsErr: errors.New("boom")}
	c := NewCached(f, time.Minute)

	if _, err := c.RecentReads(context.Background(), 10); err == nil {
		t.Fatal("want error, got nil")
	}
	f.readsErr = nil
	if _, err := c.RecentReads(context.Background(), 10); err != nil {
		t.Fatalf("second call should succeed: %v", err)
	}

	if got := atomic.LoadInt64(&f.reads); got != 2 {
		t.Errorf("RecentReads hit backend %d times, want 2 (errors must not be cached)", got)
	}
}

func TestCachedSingleFlight(t *testing.T) {
	f := &fakeSource{block: make(chan struct{})}
	c := NewCached(f, time.Minute)

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.RecentReads(context.Background(), 10)
		}()
	}

	// Give the goroutines time to pile up, then release the single backend call.
	time.Sleep(50 * time.Millisecond)
	close(f.block)
	wg.Wait()

	if got := atomic.LoadInt64(&f.reads); got != 1 {
		t.Errorf("RecentReads hit backend %d times under concurrency, want 1", got)
	}
}

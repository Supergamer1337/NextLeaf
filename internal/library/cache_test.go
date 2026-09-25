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

func TestARefreshDoesNotMakeReadersWait(t *testing.T) {
	// Refreshing ahead of need is only worth it if a reader arriving mid-fetch
	// is served what is held, rather than queued behind the fetch.
	ctx := context.Background()
	src := &fakeSource{}
	c := NewCached(src, time.Hour)
	if _, err := c.RecentReads(ctx, 0); err != nil {
		t.Fatal(err)
	}

	src.block = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := c.Refresh(ctx); done <- err }()
	for atomic.LoadInt64(&src.reads) < 2 { // the refresh is now inside the fetch
		time.Sleep(time.Millisecond)
	}

	served := make(chan struct{})
	go func() {
		if _, err := c.RecentReads(ctx, 0); err != nil {
			t.Error(err)
		}
		close(served)
	}()
	select {
	case <-served:
	case <-time.After(time.Second):
		t.Fatal("a reader waited on the refresh")
	}
	close(src.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&src.reads); got != 2 {
		t.Errorf("reads fetched %d times, want the first fetch and the refresh only", got)
	}
}

func TestAFailedRefreshKeepsWhatIsHeld(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{}
	c := NewCached(src, time.Hour)
	if _, err := c.RecentReads(ctx, 0); err != nil {
		t.Fatal(err)
	}
	src.readsErr = errors.New("down")
	if _, err := c.Refresh(ctx); err == nil {
		t.Error("a failed refresh said nothing")
	}
	got, err := c.RecentReads(ctx, 0)
	if err != nil || len(got) != 1 {
		t.Errorf("RecentReads = %v, %v; want what was held before the failed refresh", got, err)
	}
	if !c.Health().Stale {
		t.Error("data kept past a failed refresh is not reported stale")
	}
}

func TestRefreshReachesEverySourceInAMulti(t *testing.T) {
	ctx := context.Background()
	a, b := &fakeSource{}, &fakeSource{}
	src := Combine(NewCached(a, time.Hour), NewCached(b, time.Hour))
	if _, err := Refresh(ctx, src); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt64(&a.reads) != 1 || atomic.LoadInt64(&b.reads) != 1 {
		t.Errorf("reads fetched %d and %d times, want each source refreshed once", a.reads, b.reads)
	}
}

func TestARefreshSaysWhetherAnythingChanged(t *testing.T) {
	// A page only needs redrawing when the library actually moved.
	ctx := context.Background()
	src := &fakeSource{}
	c := NewCached(src, time.Hour)
	if changed, err := c.Refresh(ctx); err != nil || !changed {
		t.Errorf("first refresh: changed = %v, %v; want true, having held nothing", changed, err)
	}
	if changed, err := c.Refresh(ctx); err != nil || changed {
		t.Errorf("second refresh: changed = %v, %v; want false, the source said the same", changed, err)
	}
}

// overlapSource records the most of its fetches ever in flight at once.
type overlapSource struct {
	name        string
	active, max *int32
}

func (o overlapSource) fetch() ([]Entry, error) {
	n := atomic.AddInt32(o.active, 1)
	for {
		m := atomic.LoadInt32(o.max)
		if n <= m || atomic.CompareAndSwapInt32(o.max, m, n) {
			break
		}
	}
	time.Sleep(50 * time.Millisecond)
	atomic.AddInt32(o.active, -1)
	return []Entry{{Book: Book{Title: o.name}}}, nil
}
func (o overlapSource) Name() string                                      { return o.name }
func (o overlapSource) CurrentlyReading(context.Context) ([]Entry, error) { return o.fetch() }
func (o overlapSource) RecentReads(context.Context, int) ([]Entry, error) { return o.fetch() }
func (o overlapSource) ToRead(context.Context) ([]Entry, error)           { return o.fetch() }

func TestARefreshFetchesEveryListAtOnce(t *testing.T) {
	// Each list is its own round trip. One after another they add up to over
	// a second, and a book just added to a list waits on all of them.
	var active, most int32
	a := NewCached(overlapSource{name: "a", active: &active, max: &most}, time.Hour)
	b := NewCached(overlapSource{name: "b", active: &active, max: &most}, time.Hour)
	if _, err := Refresh(context.Background(), Combine(a, b)); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&most); got != 6 {
		t.Errorf("at most %d fetches were in flight at once, want all six lists of both sources", got)
	}
}

func TestOnceRefreshedAheadAReadNeverWaitsOnTheSource(t *testing.T) {
	// Refreshed in the background, an expired list is the refresh's to renew.
	// A read fetching for itself would put a down source's timeout into every
	// page load, and the refresh has already said the source is down.
	ctx := context.Background()
	src := &fakeSource{}
	c := NewCached(src, time.Hour)
	now := time.Now()
	c.now = func() time.Time { return now }
	if _, err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	src.block = make(chan struct{}) // down, and hanging
	defer close(src.block)

	served := make(chan struct{})
	go func() {
		defer close(served)
		for _, read := range []func() ([]Entry, error){
			func() ([]Entry, error) { return c.CurrentlyReading(ctx) },
			func() ([]Entry, error) { return c.RecentReads(ctx, 20) },
			func() ([]Entry, error) { return c.ToRead(ctx) },
		} {
			if got, err := read(); err != nil || len(got) != 1 {
				t.Errorf("read = %v, %v; want what is held", got, err)
			}
		}
	}()
	select {
	case <-served:
	case <-time.After(time.Second):
		t.Fatal("a read past the TTL waited on the source")
	}
	if r, rs, tr := atomic.LoadInt64(&src.reading), atomic.LoadInt64(&src.reads), atomic.LoadInt64(&src.toRead); r != 1 || rs != 1 || tr != 1 {
		t.Errorf("fetched %d, %d, %d times; want the refresh's fetch alone", r, rs, tr)
	}
}

// versionedSource is a fakeSource that can say cheaply whether its lists have
// changed.
type versionedSource struct {
	fakeSource
	version    string
	versionErr error
	asked      int64
}

func (v *versionedSource) Version(context.Context) (string, error) {
	atomic.AddInt64(&v.asked, 1)
	return v.version, v.versionErr
}

func (v *versionedSource) fetched() int64 {
	return atomic.LoadInt64(&v.reading) + atomic.LoadInt64(&v.reads) + atomic.LoadInt64(&v.toRead)
}

func TestARefreshKeepsWhatItHoldsWhileTheVersionHolds(t *testing.T) {
	// Asking whether anything changed costs a few hundred bytes; the lists
	// themselves cost 146KB. Most refreshes find nothing new.
	ctx := context.Background()
	src := &versionedSource{version: "v1"}
	c := NewCached(src, time.Hour)
	if _, err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if changed, err := c.Refresh(ctx); err != nil || changed {
		t.Errorf("unchanged version: changed = %v, %v; want false", changed, err)
	}
	if n := src.fetched(); n != 3 {
		t.Errorf("fetched the lists %d times, want once each: the version said nothing changed", n)
	}
	if n := atomic.LoadInt64(&src.asked); n != 2 {
		t.Errorf("asked the version %d times, want once per refresh", n)
	}

	src.version = "v2"
	if _, err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if n := src.fetched(); n != 6 {
		t.Errorf("fetched the lists %d times, want them fetched again for a new version", n)
	}
}

func TestTheListsAreFetchedAtLeastOnceATTLWhateverTheVersion(t *testing.T) {
	// A version may miss a change, a book's details say; a full fetch every
	// TTL bounds how long it can go unseen.
	ctx := context.Background()
	src := &versionedSource{version: "v1"}
	c := NewCached(src, time.Hour)
	now := time.Now()
	c.now = func() time.Time { return now }
	if _, err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Minute)
	if _, err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if n := src.fetched(); n != 3 {
		t.Fatalf("fetched %d times within the TTL, want 3", n)
	}
	now = now.Add(2 * time.Minute)
	if _, err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if n := src.fetched(); n != 6 {
		t.Errorf("fetched %d times past the TTL, want the lists fetched again", n)
	}
}

func TestAVersionThatCannotBeHadFetchesTheLists(t *testing.T) {
	ctx := context.Background()
	src := &versionedSource{version: "v1"}
	c := NewCached(src, time.Hour)
	if _, err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	src.versionErr = errors.New("hiccup")
	if _, err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if n := src.fetched(); n != 6 {
		t.Errorf("fetched %d times, want the lists fetched when the version could not be asked", n)
	}
}

func TestAFailedFetchIsNotVouchedForByItsVersion(t *testing.T) {
	// A list that failed is held from an older fetch. The same version next
	// time says nothing about that older data, so it is fetched again.
	ctx := context.Background()
	src := &versionedSource{version: "v1"}
	src.toReadErr = errors.New("down")
	c := NewCached(src, time.Hour)
	_, _ = c.Refresh(ctx)
	src.toReadErr = nil
	if _, err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt64(&src.toRead); n != 2 {
		t.Errorf("the failed list was fetched %d times, want it fetched again", n)
	}
	if h := c.Health(); h.Stale {
		t.Error("the list fetched again still reports stale")
	}
}

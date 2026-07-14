package hardcover

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnsureBearer(t *testing.T) {
	cases := map[string]string{
		"abc":        "Bearer abc",
		"Bearer abc": "Bearer abc",
		"bearer abc": "bearer abc", // already prefixed (case-insensitive)
		"  abc  ":    "Bearer abc",
		"":           "",
	}
	for in, want := range cases {
		if got := ensureBearer(in); got != want {
			t.Errorf("ensureBearer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExecuteSetsHeaders(t *testing.T) {
	var gotAuth, gotCT, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotUA = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL), WithUserAgent("nextleaf-test"))
	if err := c.execute(context.Background(), `query {}`, nil, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok")
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	if gotUA != "nextleaf-test" {
		t.Errorf("User-Agent = %q, want nextleaf-test", gotUA)
	}
}

func TestExecuteStatusErrors(t *testing.T) {
	cases := []struct {
		code int
		want error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrForbidden},
		{http.StatusTooManyRequests, ErrThrottled},
		{http.StatusInternalServerError, ErrServer},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.code)
		}))
		c := New("tok", WithEndpoint(srv.URL))
		err := c.execute(context.Background(), `query {}`, nil, nil)
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: got %v, want %v", tc.code, err, tc.want)
		}
		srv.Close()
	}
}

func TestExecuteGraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"field not found"}]}`)
	}))
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL))
	err := c.execute(context.Background(), `query {}`, nil, nil)
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestVerify(t *testing.T) {
	t.Run("accepts a valid token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"data":{"me":[{"id":42}]}}`)
		}))
		defer srv.Close()

		c := New("tok", WithEndpoint(srv.URL))
		if err := c.Verify(context.Background()); err != nil {
			t.Errorf("Verify() = %v, want nil for a valid token", err)
		}
	})

	t.Run("rejects a bad token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		c := New("bad", WithEndpoint(srv.URL))
		if err := c.Verify(context.Background()); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("Verify() = %v, want ErrUnauthorized", err)
		}
	})
}

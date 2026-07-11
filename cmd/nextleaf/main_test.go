package main

import "testing"

func TestGreeting(t *testing.T) {
	if got := greeting(); got != "Nextleaf is ready." {
		t.Fatalf("greeting() = %q", got)
	}
}

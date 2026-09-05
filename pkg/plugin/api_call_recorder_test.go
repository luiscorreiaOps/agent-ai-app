package plugin

import (
	"context"
	"sync"
	"testing"
)

func TestAPICallRecorder(t *testing.T) {
	t.Run("records method and path", func(t *testing.T) {
		ctx, rec := withAPICallRecorder(context.Background())
		recordAPICall(ctx, "GET", "/api/datasources")
		recordAPICall(ctx, "POST", "/api/ds/query")

		got := rec.snapshot()
		want := []string{"GET /api/datasources", "POST /api/ds/query"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("no recorder in context does not panic", func(t *testing.T) {
		// Non-streaming paths never create one: absence is a normal case,
		// not an error.
		recordAPICall(context.Background(), "GET", "/api/datasources")
	})

	t.Run("bounds how many calls are kept", func(t *testing.T) {
		ctx, rec := withAPICallRecorder(context.Background())
		for i := 0; i < 100; i++ {
			recordAPICall(ctx, "GET", "/api/x")
		}
		if n := len(rec.snapshot()); n > 20 {
			t.Errorf("%d calls kept, the bound must hold", n)
		}
	})

	t.Run("safe under concurrent tools", func(t *testing.T) {
		// executeToolCalls runs tool calls in parallel: each gets its own
		// recorder, but one tool can issue several requests from goroutines.
		ctx, rec := withAPICallRecorder(context.Background())
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				recordAPICall(ctx, "GET", "/api/folders")
			}()
		}
		wg.Wait()
		if n := len(rec.snapshot()); n != 10 {
			t.Errorf("got %d, want 10", n)
		}
	})
}

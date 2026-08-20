package httpapi

import (
	"testing"
)

func TestAudiencesFromAny(t *testing.T) {
	t.Parallel()

	t.Run("omitted", func(t *testing.T) {
		got, err := audiencesFromAny(nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})

	t.Run("string", func(t *testing.T) {
		got, err := audiencesFromAny(" livepeer-clearinghouse ")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != "livepeer-clearinghouse" {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("string array", func(t *testing.T) {
		got, err := audiencesFromAny([]any{"livepeer-clearinghouse", "  "})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != "livepeer-clearinghouse" {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("number", func(t *testing.T) {
		if _, err := audiencesFromAny(float64(1)); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("object", func(t *testing.T) {
		if _, err := audiencesFromAny(map[string]any{"aud": "x"}); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("mixed array", func(t *testing.T) {
		if _, err := audiencesFromAny([]any{"livepeer-clearinghouse", 1}); err == nil {
			t.Fatal("expected error")
		}
	})
}

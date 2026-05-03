package compose

import (
	"strings"
	"testing"
)

func TestBuildWindowsBelowSizeReturnsSingleWindow(t *testing.T) {
	turns := []turn{{}, {}, {}}
	windows := buildWindows(turns, 8, 4)
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1 (turns ≤ size)", len(windows))
	}
	if len(windows[0]) != 3 {
		t.Fatalf("window 0 has %d turns, want 3", len(windows[0]))
	}
}

func TestBuildWindowsExactSizeReturnsSingleWindow(t *testing.T) {
	turns := make([]turn, 8)
	windows := buildWindows(turns, 8, 4)
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1 (turns == size)", len(windows))
	}
}

func TestBuildWindowsLongConversationOverlaps(t *testing.T) {
	turns := make([]turn, 20)
	windows := buildWindows(turns, 8, 4)
	// 20 turns, size 8, stride 4: windows at 0-8, 4-12, 8-16, 12-20 = 4 windows
	if len(windows) != 4 {
		t.Fatalf("got %d windows, want 4 (20 turns, size 8, stride 4)", len(windows))
	}
	for i, w := range windows {
		if len(w) != 8 {
			t.Errorf("window %d has %d turns, want 8", i, len(w))
		}
	}
}

func TestBuildWindowsTailExtensionAbsorbsTinyTail(t *testing.T) {
	// 10 turns, size 8, stride 4: naive would be window at 0-8 then a tiny one
	// at 4-10. With the "extend if remainder < stride" rule, the first window
	// extends to cover the full 10 turns and only one window is emitted.
	turns := make([]turn, 10)
	windows := buildWindows(turns, 8, 4)
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1 (tail absorbed)", len(windows))
	}
	if len(windows[0]) != 10 {
		t.Errorf("window 0 has %d turns, want 10 (full conversation)", len(windows[0]))
	}
}

func TestBuildWindowsTwoFullWindows(t *testing.T) {
	// 12 turns, size 8, stride 4: 0-8 then 4-12. Both full size, tail aligns.
	turns := make([]turn, 12)
	windows := buildWindows(turns, 8, 4)
	if len(windows) != 2 {
		t.Fatalf("got %d windows, want 2", len(windows))
	}
	if len(windows[0]) != 8 || len(windows[1]) != 8 {
		t.Errorf("got window sizes [%d %d], want [8 8]", len(windows[0]), len(windows[1]))
	}
}

func TestDeduplicateObservationTextDropsExactDuplicates(t *testing.T) {
	input := strings.Join([]string{
		"Observation: User prefers small functions",
		"Observation: User prefers small functions",
		"Observation: Different observation",
	}, "\n\n")
	out := deduplicateObservationText(input)
	if got, want := strings.Count(out, "Observation:"), 2; got != want {
		t.Fatalf("got %d observations, want %d after dedup\noutput:\n%s", got, want, out)
	}
}

func TestDeduplicateObservationTextDropsContainedDuplicates(t *testing.T) {
	// Shorter observation is a literal substring of the longer one: longer wins.
	// Containment is character-level via strings.Contains; word reorderings or
	// inserted words don't trigger it.
	input := strings.Join([]string{
		"Observation: User prefers small functions over large ones",
		"Observation: User prefers small functions",
	}, "\n\n")
	out := deduplicateObservationText(input)
	if got, want := strings.Count(out, "Observation:"), 1; got != want {
		t.Fatalf("got %d observations, want %d (containment dedup)\noutput:\n%s", got, want, out)
	}
	if !strings.Contains(out, "over large ones") {
		t.Fatalf("expected longer observation to survive\noutput:\n%s", out)
	}
}

func TestDeduplicateObservationTextPreservesDistinct(t *testing.T) {
	input := strings.Join([]string{
		"Observation: User prefers small functions",
		"Observation: User pushes back on premature abstraction",
		"Observation: User reads complete error stack traces",
	}, "\n\n")
	out := deduplicateObservationText(input)
	if got, want := strings.Count(out, "Observation:"), 3; got != want {
		t.Fatalf("got %d observations, want %d (no overlap)\noutput:\n%s", got, want, out)
	}
}

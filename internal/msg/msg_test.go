package msg

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// A caller that adds context for its log must not change what the person
// reads: the message is found through the wrapping.
func TestOfFindsTheMessageThroughWrapping(t *testing.T) {
	err := fmt.Errorf("while saving: %w", Err("flow.noName"))
	if got := Of(err); got.Code != "flow.noName" {
		t.Fatalf("expected the wrapped message, got %+v", got)
	}
	if got := Of(errors.New("disk full")); got.Code != CodeInternal || got.Arg("text") != "disk full" {
		t.Fatalf("an uncoded failure travels as its text: %+v", got)
	}
}

func TestIsSeesTheCause(t *testing.T) {
	err := Wrap(Err("screen.noRef", "kind", "browser"), "stage.invalid", "stage", "Work")
	if !Is(err, "stage.invalid") || !Is(err, "screen.noRef") || Is(err, "flow.noName") {
		t.Fatalf("Is walks the message and its causes: %v", err)
	}
}

// What the window receives is the message and nothing else.
func TestErrorMarshalsAsItsMessage(t *testing.T) {
	b, _ := json.Marshal(Wrap(Err("b"), "a", "k", "v"))
	if string(b) != `{"code":"a","args":{"k":"v"},"cause":{"code":"b"}}` {
		t.Fatalf("unexpected JSON: %s", b)
	}
}

// A column written before messages were codes holds a sentence, and it comes
// back as that sentence.
func TestParseKeepsOldSentences(t *testing.T) {
	if got := Parse("Card dropped."); got.Code != CodeText || got.Arg("text") != "Card dropped." {
		t.Fatalf("a sentence comes back as text: %+v", got)
	}
	m := New("journal.dropped")
	if got := Parse(m.Store()); got.Code != "journal.dropped" {
		t.Fatalf("a stored message comes back as itself: %+v", got)
	}
	if !Parse("").IsZero() {
		t.Fatal("an empty column is no message")
	}
}

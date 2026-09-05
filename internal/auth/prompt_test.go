package auth

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type watchedInput struct{ read bool }

func (r *watchedInput) Read(p []byte) (int, error) { r.read = true; return 0, io.EOF }

func TestPromptDoesNotReadWhenEchoCannotBeDisabled(t *testing.T) {
	in := &watchedInput{}
	p := TerminalPrompt{In: in, Out: io.Discard, SetEcho: func(bool) error { return errors.New("unavailable") }}
	key, err := p.Ask(context.Background())
	if err == nil || key != "" || in.read {
		t.Fatal("unsafe input was allowed")
	}
}

func TestPromptRestoresEcho(t *testing.T) {
	var calls []bool
	p := TerminalPrompt{In: strings.NewReader("fixture-key\n"), Out: io.Discard, SetEcho: func(enabled bool) error { calls = append(calls, enabled); return nil }}
	key, err := p.Ask(context.Background())
	if err != nil || key != "fixture-key" || len(calls) != 2 || calls[0] || !calls[1] {
		t.Fatal("hidden input or echo restoration failed")
	}
}

func TestSaveCancelledDoesNotAccessKeychain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewKeychainStore().Save(ctx, "fixture-key"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled save was not rejected")
	}
}

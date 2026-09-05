package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type TerminalPrompt struct {
	In      io.Reader
	Out     io.Writer
	SetEcho func(enabled bool) error
}

func (p TerminalPrompt) Ask(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	in := p.In
	if in == nil {
		in = os.Stdin
	}
	out := p.Out
	if out == nil {
		out = os.Stdout
	}

	if _, err := fmt.Fprint(out, "OpenCode Go API key (input hidden): "); err != nil {
		return "", err
	}

	setEcho := p.SetEcho
	if setEcho == nil {
		setEcho = func(enabled bool) error {
			file, ok := in.(*os.File)
			if !ok {
				return nil
			}
			flag := "-echo"
			if enabled {
				flag = "echo"
			}
			cmd := exec.Command("/bin/stty", flag)
			cmd.Stdin = file
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			return cmd.Run()
		}
	}

	if err := setEcho(false); err != nil {
		return "", errors.New("cannot hide API key input; use the App settings instead")
	}
	defer func() { _ = setEcho(true) }()

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	if _, printErr := fmt.Fprintln(out); printErr != nil {
		return "", printErr
	}
	return strings.TrimSpace(line), nil
}

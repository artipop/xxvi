package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// adoptShellPath takes PATH from the person's login shell when the application
// was started by launchd rather than from a terminal. launchd hands a GUI
// application /usr/bin:/bin:/usr/sbin:/sbin, and what nvm, fnm or asdf put on
// PATH lives in the shell's rc files: without it the Node adapters are not
// found, and one that is found cannot start, because its shebang is
// `/usr/bin/env node`. Guessing the install spots one by one (acp.lookupBin)
// does not reach a version manager's per-version folders.
//
// Interactive as well as login: nvm is set up in .zshrc, which a login shell
// alone does not read. The markers cut the value out of whatever the rc files
// print on their way.
func adoptShellPath() {
	// Started from a terminal, PATH is already the shell's.
	if os.Getenv("TERM") != "" {
		return
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const mark = "__XXVI_PATH__"
	cmd := exec.CommandContext(ctx, shell, "-ilc", `printf '`+mark+`%s`+mark+`' "$PATH"`)
	// Something an rc file started in the background keeps the pipe open after
	// the shell is gone.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return
	}
	parts := strings.Split(string(out), mark)
	if len(parts) < 3 || parts[1] == "" {
		return
	}
	_ = os.Setenv("PATH", parts[1])
}

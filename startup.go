package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
)

type profile struct {
	shell, home string
	env         []string
}

// Finder's cwd and SHELL are not the user's terminal configuration.
// Read the account shell from Directory Services; let that login shell read
// its own startup files normally, inside the PTY, without an env-capture shell.
func loginProfile(ctx context.Context, inherited []string) (profile, error) {
	u, err := user.Current()
	if err != nil {
		return profile{}, fmt.Errorf("read current account: %w", err)
	}
	out, err := exec.CommandContext(ctx, "/usr/bin/dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output()
	if err != nil {
		return profile{}, fmt.Errorf("read login shell for %s: %w", u.Username, err)
	}
	shell, err := accountShell(string(out))
	if err != nil {
		return profile{}, err
	}
	return makeProfile(u.Username, u.HomeDir, shell, inherited)
}

func accountShell(record string) (string, error) {
	key, value, ok := strings.Cut(strings.TrimSpace(record), ":")
	shell := strings.TrimSpace(value)
	if !ok || key != "UserShell" || !filepath.IsAbs(shell) || strings.ContainsAny(shell, "\r\n\x00") {
		return "", fmt.Errorf("invalid account login shell: %q", record)
	}
	return shell, nil
}

func makeProfile(username, home, shell string, inherited []string) (profile, error) {
	if !filepath.IsAbs(home) {
		return profile{}, fmt.Errorf("invalid home directory: %q", home)
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return profile{}, fmt.Errorf("home directory is unavailable: %s", home)
	}
	info, err = os.Stat(shell)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return profile{}, fmt.Errorf("login shell is not executable: %s", shell)
	}
	env := slices.Clone(inherited)
	set := func(k, v string) {
		env = slices.DeleteFunc(env, func(e string) bool { return strings.HasPrefix(e, k+"=") })
		env = append(env, k+"="+v)
	}
	get := func(k string) string {
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, k+"="); ok {
				return v
			}
		}
		return ""
	}
	set("HOME", home)
	set("USER", username)
	set("LOGNAME", username)
	set("SHELL", shell)
	set("PWD", home)
	set("TERM", "xterm-256color")
	set("COLORTERM", "truecolor")
	set("TERM_PROGRAM", "Runnel")
	set("TERM_PROGRAM_VERSION", "0.1.0")
	if get("PATH") == "" {
		set("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	}
	if get("LANG") == "" && get("LC_ALL") == "" && get("LC_CTYPE") == "" {
		set("LANG", "en_US.UTF-8")
	}
	return profile{shell: shell, home: home, env: env}, nil
}

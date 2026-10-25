package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAccountShell(t *testing.T) {
	for _, input := range []string{"UserShell: /bin/zsh\n", "UserShell:\n /bin/bash\n"} {
		if shell, err := accountShell(input); err != nil || !strings.HasPrefix(shell, "/bin/") {
			t.Fatalf("%q: %q, %v", input, shell, err)
		}
	}
	for _, input := range []string{"", "UserShell: zsh", "Other: /bin/zsh", "UserShell: /bin/zsh\nextra"} {
		if _, err := accountShell(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestFinderEnvironment(t *testing.T) {
	home := t.TempDir()
	input := []string{"HOME=/wrong", "SHELL=/wrong", "PWD=/", "TERM=dumb", "TERM=duplicate", "CUSTOM=retained"}
	original := slices.Clone(input)
	p, err := makeProfile("tester", home, "/bin/zsh", input)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"HOME=" + home, "PWD=" + home, "SHELL=/bin/zsh", "USER=tester", "LOGNAME=tester", "TERM=xterm-256color", "COLORTERM=truecolor", "LANG=en_US.UTF-8", "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "CUSTOM=retained"} {
		if !slices.Contains(p.env, expected) {
			t.Errorf("missing %q", expected)
		}
	}
	for _, key := range []string{"HOME=", "SHELL=", "PWD=", "TERM="} {
		count := 0
		for _, e := range p.env {
			if strings.HasPrefix(e, key) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("%s appears %d times", key, count)
		}
	}
	if !slices.Equal(input, original) {
		t.Fatal("mutated inherited environment")
	}
	for _, locale := range []string{"LANG=lt_LT.UTF-8", "LC_ALL=C", "LC_CTYPE=UTF-8"} {
		p, err := makeProfile("tester", home, "/bin/zsh", []string{locale, "PATH=/custom/bin"})
		if err != nil || !slices.Contains(p.env, locale) || !slices.Contains(p.env, "PATH=/custom/bin") {
			t.Fatalf("lost environment: %v, %v", p.env, err)
		}
		if locale != "LANG=lt_LT.UTF-8" && slices.Contains(p.env, "LANG=en_US.UTF-8") {
			t.Fatal("overrode an explicit locale")
		}
	}
}

func TestInvalidStartupPaths(t *testing.T) {
	if _, err := makeProfile("tester", "/does-not-exist-runnel", "/bin/zsh", nil); err == nil {
		t.Fatal("accepted missing home")
	}
	if _, err := makeProfile("tester", t.TempDir(), "/missing-shell-runnel", nil); err == nil {
		t.Fatal("accepted missing shell")
	}
	if _, err := bundledLibrary(t.TempDir()); err == nil {
		t.Fatal("accepted incomplete bundle")
	}
}

func TestAccountLookupWithoutShellEnvironment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := loginProfile(ctx, []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"})
	if err != nil {
		t.Fatal(err)
	}
	if p.shell == "" || p.home == "/" {
		t.Fatalf("invalid Finder profile: %+v", p)
	}
	if _, err := os.Stat(p.shell); err != nil {
		t.Fatal(err)
	}
}

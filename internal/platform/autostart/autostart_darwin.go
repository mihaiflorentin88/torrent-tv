//go:build darwin

package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var DarwinPlistDir = defaultDarwinPlistDir

func defaultDarwinPlistDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, "Library", "LaunchAgents")
}

func plistPath() string { return filepath.Join(DarwinPlistDir(), "com.torrenttv.plist") }

// xmlEscape escapes a string for embedding in a plist XML document.
func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&#34;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

func platformEnable(opts Options) error {
	if err := os.MkdirAll(DarwinPlistDir(), 0o755); err != nil {
		return err
	}
	args := append([]string{opts.ExePath}, opts.Args...)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>com.torrenttv</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range args {
		b.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<false/>\n")
	b.WriteString("</dict>\n</plist>\n")
	return os.WriteFile(plistPath(), []byte(b.String()), 0o644)
}

func platformDisable() error {
	err := os.Remove(plistPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func platformEnabled() (bool, error) {
	_, err := os.Stat(plistPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

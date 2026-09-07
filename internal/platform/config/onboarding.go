package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// requiredKeys are the settings the server cannot do its job without: the
// download root the native engine writes into and the FileList tracker
// credentials. Everything else ships with a workable default.
var requiredKeys = []string{"downloadRoot", "fileListUsername", "fileListPasskey"}

// promptAttempts bounds how often one question repeats before startup gives
// up instead of guessing.
const promptAttempts = 3

// Console is the terminal the prompts talk to. Secret reads hidden input
// (terminal echo off); composition wires it to term.ReadPassword.
type Console struct {
	In     io.Reader
	Out    io.Writer
	Secret func() ([]byte, error)
}

// MissingRequired returns the required keys neither the settings file nor
// the environment provides, in prompt order. A value inherited from the
// built-in defaults counts as missing: it is exactly what crashed startup
// when the default download root was unwritable.
func (s *Store) MissingRequired() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var missing []string
	for _, key := range requiredKeys {
		if (key == "fileListUsername" || key == "fileListPasskey") && !s.value.FileListEnabled {
			continue
		}
		if strings.TrimSpace(requiredValue(s.value, key)) == "" || !(s.fileProvided[key] || s.envManaged[key]) {
			missing = append(missing, key)
		}
	}
	return missing
}

// PromptRequired asks for each missing required setting, validates every
// answer, and stores them into the settings file before startup continues.
// A blank answer accepts the shown default where one exists. With tty=false
// there is nobody to answer, so it is a no-op and the caller warns instead.
func PromptRequired(s *Store, c Console, tty bool) error {
	if !tty {
		return nil
	}
	missing := s.MissingRequired()
	filtered := missing[:0]
	enabled := s.Get().FileListEnabled
	for _, key := range missing {
		if (key == "fileListUsername" || key == "fileListPasskey") && !enabled {
			continue
		}
		filtered = append(filtered, key)
	}
	missing = filtered
	if len(missing) == 0 {
		return nil
	}
	reader := bufio.NewReader(c.In)
	answers := make(map[string]string, len(missing))
	for _, key := range missing {
		answer, err := askRequired(s, c, reader, key)
		if err != nil {
			return err
		}
		answers[key] = answer
	}
	next := s.Get()
	if answer, ok := answers["downloadRoot"]; ok {
		next.DownloadRoot = answer
	}
	if answer, ok := answers["fileListUsername"]; ok {
		next.FileListUsername = answer
	}
	if answer, ok := answers["fileListPasskey"]; ok {
		next.FileListPasskey = answer
	}
	if err := s.Save(next); err != nil {
		return fmt.Errorf("save prompted settings: %w", err)
	}
	s.mu.Lock()
	for key := range answers {
		s.fileProvided[key] = true
	}
	s.mu.Unlock()
	return nil
}

// askRequired repeats one question until the answer validates or the attempt
// budget runs out.
func askRequired(s *Store, c Console, reader *bufio.Reader, key string) (string, error) {
	for attempt := 1; attempt <= promptAttempts; attempt++ {
		answer, err := askOnce(s, c, reader, key)
		if err != nil {
			return "", err
		}
		problem := checkAnswer(key, answer)
		if problem == nil {
			return answer, nil
		}
		fmt.Fprintf(c.Out, "%s; try again (%d of %d used)\n", problem, attempt, promptAttempts)
	}
	return "", fmt.Errorf("no valid answer for %s after %d attempts", key, promptAttempts)
}

func askOnce(s *Store, c Console, reader *bufio.Reader, key string) (string, error) {
	switch key {
	case "downloadRoot":
		fmt.Fprintf(c.Out, "Download root [%s]: ", Defaults().DownloadRoot)
		line, err := readLine(reader)
		if err != nil {
			return "", err
		}
		if line == "" {
			line = Defaults().DownloadRoot
		}
		return line, nil
	case "fileListUsername":
		fmt.Fprint(c.Out, "FileList username: ")
		return readLine(reader)
	case "fileListPasskey":
		fmt.Fprint(c.Out, "FileList passkey: ")
		if c.Secret == nil {
			return "", errors.New("no hidden-input reader available")
		}
		secret, err := c.Secret()
		fmt.Fprintln(c.Out) // ReadPassword does not echo the newline
		if err != nil {
			return "", err
		}
		return string(secret), nil
	}
	return "", fmt.Errorf("unknown required key %q", key)
}

func checkAnswer(key, answer string) error {
	switch key {
	case "downloadRoot":
		if strings.TrimSpace(answer) == "" {
			return errors.New("download root must not be empty")
		}
		if err := ensureRootWritable(answer); err != nil {
			return fmt.Errorf("download root %q is not usable: %w", answer, err)
		}
		return nil
	case "fileListUsername":
		if strings.TrimSpace(answer) == "" {
			return errors.New("username must not be empty")
		}
		return nil
	case "fileListPasskey":
		if strings.TrimSpace(answer) == "" {
			return errors.New("passkey must not be empty")
		}
		return nil
	}
	return fmt.Errorf("unknown required key %q", key)
}

// ensureRootWritable mirrors the httpapi settings-save probe: create the
// directory when missing and prove it accepts writes, so the prompt fails
// exactly when the native engine would at startup.
func ensureRootWritable(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	probe, err := os.CreateTemp(root, ".write-probe-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func requiredValue(v Settings, key string) string {
	switch key {
	case "downloadRoot":
		return v.DownloadRoot
	case "fileListUsername":
		return v.FileListUsername
	case "fileListPasskey":
		return v.FileListPasskey
	}
	return ""
}

// ResolveMediaTools fills ffprobePath and ffmpegPath from PATH when the
// configured paths do not exist on disk and the environment does not manage
// them, persisting discoveries so later starts skip the lookup. Existing
// usable paths are trusted. It returns the tools still missing after the
// lookup; a missing tool warns but never blocks startup, matching how the
// probe degrades at runtime.
func (s *Store) ResolveMediaTools() ([]string, error) {
	type mediaTool struct {
		key, binary, current string
		managed              bool
	}
	s.mu.RLock()
	tools := []mediaTool{
		{key: "ffprobePath", binary: "ffprobe", current: s.value.FFprobePath, managed: s.envManaged["ffprobePath"]},
		{key: "ffmpegPath", binary: "ffmpeg", current: s.value.FFmpegPath, managed: s.envManaged["ffmpegPath"]},
	}
	s.mu.RUnlock()

	discovered := map[string]string{}
	var missing []string
	for _, tool := range tools {
		if tool.managed {
			continue // the environment overlay is authoritative
		}
		if _, err := os.Stat(tool.current); err == nil {
			continue // a configured path that exists is the user's choice
		}
		found, err := exec.LookPath(tool.binary)
		if err != nil {
			missing = append(missing, tool.binary)
			continue
		}
		discovered[tool.key] = found
	}
	if len(discovered) == 0 {
		return missing, nil
	}
	next := s.Get()
	if found, ok := discovered["ffprobePath"]; ok {
		next.FFprobePath = found
	}
	if found, ok := discovered["ffmpegPath"]; ok {
		next.FFmpegPath = found
	}
	if err := s.Save(next); err != nil {
		return missing, fmt.Errorf("save discovered media tools: %w", err)
	}
	return missing, nil
}

package rotatingfile

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"megaapp-back/internal/platform/appendfile"
)

func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

func glob(t *testing.T, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("Glob(%q) error = %v", pattern, err)
	}
	sort.Strings(matches)
	return matches
}

func readZipEntry(t *testing.T, path string) string {
	t.Helper()
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("zip.OpenReader(%q) error = %v", path, err)
	}
	defer archive.Close()
	if len(archive.File) != 1 {
		t.Fatalf("archive %q has %d entries, want 1", path, len(archive.File))
	}
	entry, err := archive.File[0].Open()
	if err != nil {
		t.Fatalf("open archive entry: %v", err)
	}
	defer entry.Close()
	data, err := io.ReadAll(entry)
	if err != nil {
		t.Fatalf("read archive entry: %v", err)
	}
	return string(data)
}

func mustAppend(t *testing.T, w *Writer, lines string) {
	t.Helper()
	if err := w.Append([]byte(lines)); err != nil {
		t.Fatalf("Append(%q) error = %v", lines, err)
	}
}

func TestAppendAccumulatesInOneActiveFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	w := New(dir, "events", 1<<20)

	mustAppend(t, w, "one\n")
	mustAppend(t, w, "two\n")

	files := glob(t, filepath.Join(dir, "events-*.ndjson"))
	if len(files) != 1 {
		t.Fatalf("active files = %v, want exactly one", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "one\ntwo\n" {
		t.Fatalf("file = %q, want %q", data, "one\ntwo\n")
	}
}

func TestAppendIgnoresEmptyInput(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "events", 10)

	if err := w.Append(nil); err != nil {
		t.Fatalf("Append(nil) error = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("directory has %d entries after an empty append, want 0", len(entries))
	}
}

func TestRotatesBeforeTheLimitIsExceeded(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "events", 10)

	mustAppend(t, w, "12345678")
	mustAppend(t, w, "abc")
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	archives := glob(t, filepath.Join(dir, archiveDirName, "events-*--*.ndjson.zip"))
	if len(archives) != 1 {
		t.Fatalf("archives = %v, want exactly one", archives)
	}
	if got := readZipEntry(t, archives[0]); got != "12345678" {
		t.Fatalf("archived content = %q, want %q", got, "12345678")
	}
	if closed := glob(t, filepath.Join(dir, "events-*--*.ndjson")); len(closed) != 0 {
		t.Fatalf("closed files left after archiving: %v", closed)
	}
	active := glob(t, filepath.Join(dir, "events-*.ndjson"))
	if len(active) != 1 {
		t.Fatalf("active files = %v, want exactly one", active)
	}
	if data, _ := os.ReadFile(active[0]); string(data) != "abc" {
		t.Fatalf("active content = %q, want %q", data, "abc")
	}
}

func TestResumesTheActiveFileAfterRestart(t *testing.T) {
	dir := t.TempDir()
	first := New(dir, "events", 20)
	mustAppend(t, first, "12345678")

	second := New(dir, "events", 20)
	mustAppend(t, second, "abc")
	mustAppend(t, second, "defghijklmnop")
	if err := second.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	archives := glob(t, filepath.Join(dir, archiveDirName, "events-*.zip"))
	if len(archives) != 1 {
		t.Fatalf("archives = %v, want exactly one", archives)
	}
	if got := readZipEntry(t, archives[0]); got != "12345678abc" {
		t.Fatalf("archived content = %q, want %q", got, "12345678abc")
	}
}

func TestArchivesAClosedFileLeftByACrash(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, "events-2000-01-02T03-04-05.000--2000-01-02T04-04-05.000.ndjson")
	if err := os.WriteFile(orphan, []byte("left behind\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	w := New(dir, "events", 1<<20)

	mustAppend(t, w, "fresh\n")
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan still present, err = %v", err)
	}
	archives := glob(t, filepath.Join(dir, archiveDirName, "*.zip"))
	if len(archives) != 1 {
		t.Fatalf("archives = %v, want exactly one", archives)
	}
	if got := readZipEntry(t, archives[0]); got != "left behind\n" {
		t.Fatalf("archived content = %q", got)
	}
}

func TestRotationsInTheSameInstantKeepEveryFile(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "events", 5)
	w.now = fixedClock(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))

	for _, lines := range []string{"aaaa", "bbbb", "cccc", "dddd"} {
		mustAppend(t, w, lines)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	archives := glob(t, filepath.Join(dir, archiveDirName, "*.zip"))
	if len(archives) != 3 {
		t.Fatalf("archives = %v, want 3 (every rotation kept)", archives)
	}
	var contents []string
	for _, archive := range archives {
		contents = append(contents, readZipEntry(t, archive))
	}
	sort.Strings(contents)
	if got := contents[0] + contents[1] + contents[2]; got != "aaaabbbbcccc" {
		t.Fatalf("archived contents = %v, want aaaa, bbbb, cccc", contents)
	}
}

func TestWritersWithDifferentPrefixesDoNotTouchEachOther(t *testing.T) {
	dir := t.TempDir()
	events := New(dir, "events", 1<<20)
	other := New(dir, "other", 1<<20)

	mustAppend(t, events, "e\n")
	mustAppend(t, other, "o\n")
	again := New(dir, "events", 1<<20)
	mustAppend(t, again, "e2\n")

	eventFiles := glob(t, filepath.Join(dir, "events-*.ndjson"))
	if len(eventFiles) != 1 {
		t.Fatalf("events files = %v, want one", eventFiles)
	}
	if data, _ := os.ReadFile(eventFiles[0]); string(data) != "e\ne2\n" {
		t.Fatalf("events content = %q", data)
	}
	otherFiles := glob(t, filepath.Join(dir, "other-*.ndjson"))
	if len(otherFiles) != 1 {
		t.Fatalf("other files = %v, want one", otherFiles)
	}
	if data, _ := os.ReadFile(otherFiles[0]); string(data) != "o\n" {
		t.Fatalf("other content = %q", data)
	}
}

func tornWrite(keep int) func(path string, data []byte) error {
	return func(path string, data []byte) error {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := file.Write(data[:keep]); err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return errors.New("no space left on device")
	}
}

func activeContent(t *testing.T, dir string) string {
	t.Helper()
	files := glob(t, filepath.Join(dir, "events-*.ndjson"))
	if len(files) != 1 {
		t.Fatalf("active files = %v, want exactly one", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(data)
}

func TestAFailedWriteLeavesNoTornLineBehind(t *testing.T) {
	tests := []struct {
		name     string
		before   string
		wantFile string
	}{
		{name: "after earlier lines", before: "first\n", wantFile: "first\n"},
		{name: "in a file that did not exist yet", before: "", wantFile: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			w := New(dir, "events", 1<<20)
			if tt.before != "" {
				mustAppend(t, w, tt.before)
			}

			realAppend := w.appendFile
			w.appendFile = tornWrite(4)
			if err := w.Append([]byte("second\n")); err == nil {
				t.Fatal("Append() error = nil, want the write failure")
			}
			if got := activeContent(t, dir); got != tt.wantFile {
				t.Fatalf("file after the failure = %q, want %q", got, tt.wantFile)
			}

			w.appendFile = realAppend
			mustAppend(t, w, "third\n")
			if got, want := activeContent(t, dir), tt.wantFile+"third\n"; got != want {
				t.Fatalf("file after the retry = %q, want %q: every line must be whole", got, want)
			}
		})
	}
}

func TestAFailedWriteThatCannotBeCutBackClosesTheFile(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "events", 1<<20)
	mustAppend(t, w, "first\n")
	w.appendFile = tornWrite(4)
	w.truncate = func(string, int64) error { return errors.New("read-only file system") }

	err := w.Append([]byte("second\n"))

	if err == nil || !strings.Contains(err.Error(), "no space left on device") || !strings.Contains(err.Error(), "read-only file system") {
		t.Fatalf("Append() error = %v, want both the write failure and the failed cut", err)
	}

	w.appendFile = appendfile.Append
	mustAppend(t, w, "third\n")
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	archives := glob(t, filepath.Join(dir, archiveDirName, "events-*.zip"))
	if len(archives) != 1 {
		t.Fatalf("archives = %v, want the damaged file closed into one", archives)
	}
	if got := readZipEntry(t, archives[0]); got != "first\nseco" {
		t.Fatalf("archived content = %q, want the torn tail at the very end", got)
	}
	if got := activeContent(t, dir); got != "third\n" {
		t.Fatalf("new file = %q, want only the line written after the failure", got)
	}
}

func TestAppendFailsWhenTheDirectoryCannotBeCreated(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	w := New(filepath.Join(blocker, "sub"), "events", 1<<20)

	if err := w.Append([]byte("one\n")); err == nil {
		t.Fatal("Append() error = nil, want an error")
	}
}

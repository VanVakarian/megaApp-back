package rotatingfile

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"megaapp-back/internal/platform/appendfile"
)

const (
	archiveDirName = "logs-archive"
	timeLayout     = "2006-01-02T15-04-05.000"
	timePattern    = `\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}\.\d{3}`
)

type Writer struct {
	dir        string
	prefix     string
	maxBytes   int64
	now        func() time.Time
	appendFile func(path string, data []byte) error
	truncate   func(path string, size int64) error

	active *regexp.Regexp
	closed *regexp.Regexp

	mu       sync.Mutex
	ready    bool
	openedAt string
	size     int64

	archiving sync.WaitGroup
}

func New(dir, prefix string, maxBytes int64) *Writer {
	quoted := regexp.QuoteMeta(prefix)
	return &Writer{
		dir:      dir,
		prefix:   prefix,
		maxBytes: maxBytes,
		now:      func() time.Time { return time.Now().UTC() },

		appendFile: appendfile.Append,
		truncate:   os.Truncate,

		active: regexp.MustCompile(`^` + quoted + `-(` + timePattern + `)\.ndjson$`),
		closed: regexp.MustCompile(`^` + quoted + `-` + timePattern + `--` + timePattern + `\.ndjson$`),
	}
}

func (w *Writer) Append(lines []byte) error {
	if len(lines) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.ready {
		if err := w.resume(); err != nil {
			return err
		}
		w.ready = true
	}

	if w.size > 0 && w.size+int64(len(lines)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return err
		}
	}
	if w.openedAt == "" {
		w.openedAt = w.nextOpenedAt("")
	}

	if err := w.appendFile(w.activePath(), lines); err != nil {
		return errors.Join(err, w.discardPartialWrite())
	}
	w.size += int64(len(lines))
	return nil
}

func (w *Writer) discardPartialWrite() error {
	err := w.truncate(w.activePath(), w.size)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return errors.Join(fmt.Errorf("cut back a partial write: %w", err), w.rotate())
}

func (w *Writer) Close() error {
	w.archiving.Wait()
	return nil
}

func (w *Writer) activePath() string {
	return filepath.Join(w.dir, w.prefix+"-"+w.openedAt+".ndjson")
}

func (w *Writer) resume() error {
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return fmt.Errorf("create ingest dir: %w", err)
	}
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return fmt.Errorf("read ingest dir: %w", err)
	}

	var opened []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if w.closed.MatchString(entry.Name()) {
			w.archiveAsync(filepath.Join(w.dir, entry.Name()))
		} else if match := w.active.FindStringSubmatch(entry.Name()); match != nil {
			opened = append(opened, match[1])
		}
	}

	w.openedAt, w.size = "", 0
	if len(opened) == 0 {
		return nil
	}
	sort.Strings(opened)
	w.openedAt = opened[len(opened)-1]
	info, err := os.Stat(w.activePath())
	if err != nil {
		return fmt.Errorf("stat ingest file: %w", err)
	}
	w.size = info.Size()
	return nil
}

func (w *Writer) rotate() error {
	closedPath := filepath.Join(w.dir, fmt.Sprintf("%s-%s--%s.ndjson", w.prefix, w.openedAt, w.now().Format(timeLayout)))
	if err := os.Rename(w.activePath(), closedPath); err != nil {
		return fmt.Errorf("close ingest file: %w", err)
	}
	w.archiveAsync(closedPath)
	w.openedAt = w.nextOpenedAt(w.openedAt)
	w.size = 0
	return nil
}

func (w *Writer) nextOpenedAt(previous string) string {
	next := w.now()
	if previous != "" {
		if previousAt, err := time.Parse(timeLayout, previous); err == nil && !next.After(previousAt) {
			next = previousAt.Add(time.Millisecond)
		}
	}
	return next.Format(timeLayout)
}

func (w *Writer) archiveAsync(closedPath string) {
	w.archiving.Add(1)
	go func() {
		defer w.archiving.Done()
		if err := archive(closedPath); err != nil {
			slog.Error("ingest_archive_failed", "path", closedPath, "err", err)
		}
	}()
}

func archive(closedPath string) error {
	archiveDir := filepath.Join(filepath.Dir(closedPath), archiveDirName)
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}
	zipPath := filepath.Join(archiveDir, filepath.Base(closedPath)+".zip")
	tempPath := zipPath + ".tmp"
	if err := zipFile(closedPath, tempPath); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, zipPath); err != nil {
		return fmt.Errorf("publish archive: %w", err)
	}
	if err := os.Remove(closedPath); err != nil {
		return fmt.Errorf("remove archived file: %w", err)
	}
	return nil
}

func zipFile(srcPath, zipPath string) (err error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open file to archive: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(zipPath)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer func() { err = errors.Join(err, dst.Close()) }()

	zw := zip.NewWriter(dst)
	entry, err := zw.CreateHeader(&zip.FileHeader{Name: filepath.Base(srcPath), Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return errors.Join(fmt.Errorf("create archive entry: %w", err), zw.Close())
	}
	if _, err := io.Copy(entry, src); err != nil {
		return errors.Join(fmt.Errorf("write archive: %w", err), zw.Close())
	}
	return zw.Close()
}

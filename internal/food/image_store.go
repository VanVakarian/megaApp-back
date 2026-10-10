package food

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var (
	imageThumbPattern    = regexp.MustCompile(`^(\d+)-thumb-v(\d+)\.webp$`)
	imageVariantPattern  = regexp.MustCompile(`^(\d+)-(thumb|medium|large|squircle|corner)-v(\d+)\.(webp|png)$`)
	imageOriginalPattern = regexp.MustCompile(`^(\d+)-original-v(\d+)\.(png|jpg|jpeg|webp)$`)
)

type ImageStore struct {
	foodDir  string
	origDir  string
	mu       sync.RWMutex
	versions map[int64]int64
}

func NewImageStore(publicDir string) (*ImageStore, error) {
	foodDir := filepath.Join(publicDir, "images", "food")
	origDir := filepath.Join(foodDir, "orig")
	if err := os.MkdirAll(origDir, 0o755); err != nil {
		return nil, fmt.Errorf("create image directories: %w", err)
	}
	store := &ImageStore{foodDir: foodDir, origDir: origDir, versions: map[int64]int64{}}
	if err := store.reload(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *ImageStore) reload() error {
	entries, err := os.ReadDir(s.foodDir)
	if err != nil {
		return fmt.Errorf("read image directory: %w", err)
	}

	versions := make(map[int64]int64)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := imageThumbPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		catalogueID, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			continue
		}
		version, err := strconv.ParseInt(match[2], 10, 64)
		if err != nil {
			continue
		}
		if version > versions[catalogueID] {
			versions[catalogueID] = version
		}
	}

	s.mu.Lock()
	s.versions = versions
	s.mu.Unlock()
	return nil
}

func (s *ImageStore) ImageVersion(catalogueID int64) *int64 {
	s.mu.RLock()
	version, ok := s.versions[catalogueID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	value := version
	return &value
}

func (s *ImageStore) SetImageVersion(catalogueID int64, version int64) {
	s.mu.Lock()
	s.versions[catalogueID] = version
	s.mu.Unlock()
}

func (s *ImageStore) SaveOriginal(catalogueID int64, version int64, format string, data []byte) (string, error) {
	filename := fmt.Sprintf("%d-original-v%d.%s", catalogueID, version, strings.ToLower(strings.TrimPrefix(format, ".")))
	path := filepath.Join(s.origDir, filename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write original image: %w", err)
	}
	return filename, nil
}

func (s *ImageStore) VariantPath(filename string) (string, error) {
	if imageVariantPattern.FindStringSubmatch(filename) == nil {
		return "", fmt.Errorf("invalid image filename")
	}
	return filepath.Join(s.foodDir, filename), nil
}

func (s *ImageStore) FindLatestOriginal(catalogueID int64) (string, string, int64, error) {
	entries, err := os.ReadDir(s.origDir)
	if err != nil {
		return "", "", 0, fmt.Errorf("read original directory: %w", err)
	}

	var latestName string
	var latestVersion int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := imageOriginalPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		id, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || id != catalogueID {
			continue
		}
		version, err := strconv.ParseInt(match[2], 10, 64)
		if err != nil {
			continue
		}
		if version > latestVersion {
			latestVersion = version
			latestName = entry.Name()
		}
	}

	if latestName == "" {
		return "", "", 0, os.ErrNotExist
	}
	return latestName, filepath.Join(s.origDir, latestName), latestVersion, nil
}

func (s *ImageStore) RenameOriginal(oldName string, catalogueID int64, version int64) (string, error) {
	match := imageOriginalPattern.FindStringSubmatch(oldName)
	if match == nil {
		return "", fmt.Errorf("invalid original image filename")
	}
	newName := fmt.Sprintf("%d-original-v%d.%s", catalogueID, version, match[3])
	oldPath := filepath.Join(s.origDir, oldName)
	newPath := filepath.Join(s.origDir, newName)
	if err := os.Rename(oldPath, newPath); err != nil {
		return "", fmt.Errorf("rename original image: %w", err)
	}
	return newName, nil
}

func (s *ImageStore) DeleteOldVersions(catalogueID int64, keepVersion int64) error {
	if err := s.deleteOldVariants(catalogueID, keepVersion); err != nil {
		return err
	}
	if err := s.deleteOldOriginals(catalogueID, keepVersion); err != nil {
		return err
	}
	return nil
}

func (s *ImageStore) deleteOldVariants(catalogueID int64, keepVersion int64) error {
	entries, err := os.ReadDir(s.foodDir)
	if err != nil {
		return fmt.Errorf("read image variants: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := imageVariantPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		id, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || id != catalogueID {
			continue
		}
		version, err := strconv.ParseInt(match[3], 10, 64)
		if err != nil || version == keepVersion {
			continue
		}
		if err := os.Remove(filepath.Join(s.foodDir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete old image variant: %w", err)
		}
	}
	return nil
}

func (s *ImageStore) deleteOldOriginals(catalogueID int64, keepVersion int64) error {
	entries, err := os.ReadDir(s.origDir)
	if err != nil {
		return fmt.Errorf("read original images: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := imageOriginalPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		id, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || id != catalogueID {
			continue
		}
		version, err := strconv.ParseInt(match[2], 10, 64)
		if err != nil || version == keepVersion {
			continue
		}
		if err := os.Remove(filepath.Join(s.origDir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete old original image: %w", err)
		}
	}
	return nil
}

func (s *ImageStore) CatalogueIDsWithOriginals() ([]int64, error) {
	entries, err := os.ReadDir(s.origDir)
	if err != nil {
		return nil, fmt.Errorf("read original image directory: %w", err)
	}
	idsMap := map[int64]struct{}{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := imageOriginalPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		id, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			continue
		}
		idsMap[id] = struct{}{}
	}
	ids := make([]int64, 0, len(idsMap))
	for id := range idsMap {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i int, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func (s *ImageStore) HasAllVariants(catalogueID int64) (bool, error) {
	entries, err := os.ReadDir(s.foodDir)
	if err != nil {
		return false, fmt.Errorf("read image variants directory: %w", err)
	}
	found := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := imageVariantPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		id, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || id != catalogueID {
			continue
		}
		found[match[2]] = true
	}
	return found["thumb"] && found["medium"] && found["large"] && found["squircle"] && found["corner"], nil
}

func (s *ImageStore) FoodDir() string {
	return s.foodDir
}

func (s *ImageStore) Close() error {
	return nil
}

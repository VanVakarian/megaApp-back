package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	ingestEnvPrefix  = "INGEST_"
	ingestSourcesEnv = "INGEST_SOURCES"
	ingestKeysEnv    = "INGEST_KEYS"

	minIngestRotateBytes      int64 = 1_000_000
	maxIngestSourceNameLength       = 32
	minIngestKeyLength              = 24
	maxIngestKeyLength              = 128
)

var (
	ingestSourceNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	ingestKeyPattern        = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	ingestSizePattern       = regexp.MustCompile(`^(\d{1,15})\s*(B|KB|MB|GB)?$`)
)

type IngestSource struct {
	Name        string
	RotateBytes int64
}

func loadIngest() ([]IngestSource, []string, error) {
	if err := rejectUnknownIngestVariables(); err != nil {
		return nil, nil, err
	}
	sources, err := parseIngestSources(os.Getenv(ingestSourcesEnv))
	if err != nil {
		return nil, nil, err
	}
	keys, err := parseIngestKeys(os.Getenv(ingestKeysEnv))
	if err != nil {
		return nil, nil, err
	}
	return sources, keys, nil
}

func parseIngestSources(raw string) ([]IngestSource, error) {
	var sources []IngestSource
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		name, size, found := strings.Cut(entry, ":")
		name = strings.TrimSpace(name)
		if !found {
			return nil, fmt.Errorf("load config: %s: %q must look like name:size, for example %s:100MB", ingestSourcesEnv, entry, name)
		}
		if len(name) > maxIngestSourceNameLength || !ingestSourceNamePattern.MatchString(name) {
			return nil, fmt.Errorf("load config: %s: %q is not a valid source name (lowercase letters and digits, words joined by dashes, up to %d characters)", ingestSourcesEnv, name, maxIngestSourceNameLength)
		}
		if seen[name] {
			return nil, fmt.Errorf("load config: %s lists %q twice", ingestSourcesEnv, name)
		}
		seen[name] = true

		rotateBytes, err := parseByteSize(size)
		if err != nil {
			return nil, fmt.Errorf("load config: %s: source %q: %w", ingestSourcesEnv, name, err)
		}
		if rotateBytes < minIngestRotateBytes {
			return nil, fmt.Errorf("load config: %s: source %q: size must be at least 1MB", ingestSourcesEnv, name)
		}
		sources = append(sources, IngestSource{Name: name, RotateBytes: rotateBytes})
	}
	return sources, nil
}

func parseIngestKeys(raw string) ([]string, error) {
	var keys []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		key := strings.TrimSpace(part)
		if key == "" {
			continue
		}
		position := len(keys) + 1
		if len(key) < minIngestKeyLength || len(key) > maxIngestKeyLength || !ingestKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("load config: %s: key #%d must be %d-%d letters and digits", ingestKeysEnv, position, minIngestKeyLength, maxIngestKeyLength)
		}
		if seen[key] {
			return nil, fmt.Errorf("load config: %s: key #%d repeats an earlier one", ingestKeysEnv, position)
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys, nil
}

func parseByteSize(raw string) (int64, error) {
	match := ingestSizePattern.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(raw)))
	if match == nil {
		return 0, fmt.Errorf("%q is not a size (a number with B, KB, MB or GB)", raw)
	}
	number, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size: %w", raw, err)
	}
	multiplier := map[string]int64{"": 1, "B": 1, "KB": 1_000, "MB": 1_000_000, "GB": 1_000_000_000}[match[2]]
	return number * multiplier, nil
}

func rejectUnknownIngestVariables() error {
	var unknown []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, ingestEnvPrefix) && name != ingestSourcesEnv && name != ingestKeysEnv {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("load config: unexpected %s: only %s and %s exist", strings.Join(unknown, ", "), ingestSourcesEnv, ingestKeysEnv)
}

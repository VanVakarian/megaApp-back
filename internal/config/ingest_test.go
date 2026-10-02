package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

const (
	testKeyA = "aaaaaaaaaaaaaaaaaaaaaaaaAAAA1111"
	testKeyB = "bbbbbbbbbbbbbbbbbbbbbbbbBBBB2222"
)

func setIngestEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, ingestEnvPrefix) {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatalf("Unsetenv(%s) error = %v", name, err)
			}
		}
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func TestLoadIngestAcceptsValidConfigurations(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantSources []IngestSource
		wantKeys    []string
	}{
		{
			name: "nothing configured turns the intake off",
			env:  map[string]string{},
		},
		{
			name: "the production shape",
			env: map[string]string{
				"INGEST_SOURCES": "telemetry:1GB,hh-auto-otclicker:100MB",
				"INGEST_KEYS":    testKeyA,
			},
			wantSources: []IngestSource{
				{Name: "telemetry", RotateBytes: 1_000_000_000},
				{Name: "hh-auto-otclicker", RotateBytes: 100_000_000},
			},
			wantKeys: []string{testKeyA},
		},
		{
			name: "no keys: only sessions get in",
			env:  map[string]string{"INGEST_SOURCES": "telemetry:1GB"},
			wantSources: []IngestSource{
				{Name: "telemetry", RotateBytes: 1_000_000_000},
			},
		},
		{
			name: "spaces, lowercase units and a bare number of bytes",
			env:  map[string]string{"INGEST_SOURCES": " one : 5mb , two:123456789 , three:2 GB "},
			wantSources: []IngestSource{
				{Name: "one", RotateBytes: 5_000_000},
				{Name: "two", RotateBytes: 123_456_789},
				{Name: "three", RotateBytes: 2_000_000_000},
			},
		},
		{
			name:     "several keys let a key be replaced without downtime",
			env:      map[string]string{"INGEST_KEYS": testKeyA + " , " + testKeyB},
			wantKeys: []string{testKeyA, testKeyB},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setIngestEnv(t, tt.env)

			sources, keys, err := loadIngest()
			if err != nil {
				t.Fatalf("loadIngest() error = %v", err)
			}
			if !reflect.DeepEqual(sources, tt.wantSources) {
				t.Fatalf("sources = %+v, want %+v", sources, tt.wantSources)
			}
			if !reflect.DeepEqual(keys, tt.wantKeys) {
				t.Fatalf("keys = %v, want %v", keys, tt.wantKeys)
			}
		})
	}
}

func TestLoadIngestRejectsMistakes(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "a source without a size",
			env:     map[string]string{"INGEST_SOURCES": "telemetry"},
			wantErr: "must look like name:size",
		},
		{
			name:    "an empty size",
			env:     map[string]string{"INGEST_SOURCES": "telemetry:"},
			wantErr: `source "telemetry"`,
		},
		{
			name:    "uppercase source name",
			env:     map[string]string{"INGEST_SOURCES": "Telemetry:1GB"},
			wantErr: "not a valid source name",
		},
		{
			name:    "underscore in a source name",
			env:     map[string]string{"INGEST_SOURCES": "hh_auto:1GB"},
			wantErr: "not a valid source name",
		},
		{
			name:    "source name too long",
			env:     map[string]string{"INGEST_SOURCES": strings.Repeat("a", 33) + ":1GB"},
			wantErr: "not a valid source name",
		},
		{
			name:    "source listed twice",
			env:     map[string]string{"INGEST_SOURCES": "one:1GB,one:2GB"},
			wantErr: "twice",
		},
		{
			name:    "size below the minimum",
			env:     map[string]string{"INGEST_SOURCES": "one:500KB"},
			wantErr: "at least 1MB",
		},
		{
			name:    "size that is not a size",
			env:     map[string]string{"INGEST_SOURCES": "one:lots"},
			wantErr: `source "one"`,
		},
		{
			name:    "unit that does not exist",
			env:     map[string]string{"INGEST_SOURCES": "one:1TB"},
			wantErr: `source "one"`,
		},
		{
			name:    "key too short",
			env:     map[string]string{"INGEST_KEYS": "short"},
			wantErr: "INGEST_KEYS: key #1 must be",
		},
		{
			name:    "key with characters that break env lines and headers",
			env:     map[string]string{"INGEST_KEYS": testKeyA + ",bbbbbbbbbbbbbbbbbbbbbbbb!!!!"},
			wantErr: "key #2 must be",
		},
		{
			name:    "key repeated",
			env:     map[string]string{"INGEST_KEYS": testKeyA + "," + testKeyA},
			wantErr: "key #2 repeats",
		},
		{
			name:    "the variables of the old per-source scheme",
			env:     map[string]string{"INGEST_SOURCES": "one:1GB", "INGEST_ONE_KEYS": testKeyA},
			wantErr: "unexpected INGEST_ONE_KEYS",
		},
		{
			name:    "the old shared rotation variable",
			env:     map[string]string{"INGEST_SOURCES": "one:1GB", "INGEST_ROTATE": "100MB"},
			wantErr: "unexpected INGEST_ROTATE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setIngestEnv(t, tt.env)

			_, _, err := loadIngest()
			if err == nil {
				t.Fatal("loadIngest() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
			}
			for _, key := range []string{testKeyA, testKeyB} {
				if strings.Contains(err.Error(), key) {
					t.Fatalf("error %q leaks a key", err)
				}
			}
		})
	}
}

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		raw     string
		want    int64
		wantErr bool
	}{
		{raw: "100MB", want: 100_000_000},
		{raw: "100mb", want: 100_000_000},
		{raw: "1 GB", want: 1_000_000_000},
		{raw: "500KB", want: 500_000},
		{raw: "2048B", want: 2048},
		{raw: "2048", want: 2048},
		{raw: "", wantErr: true},
		{raw: "MB", wantErr: true},
		{raw: "-5MB", wantErr: true},
		{raw: "1.5GB", wantErr: true},
		{raw: "10TB", wantErr: true},
		{raw: "1234567890123456MB", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := parseByteSize(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseByteSize(%q) = %d, want an error", tt.raw, got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("parseByteSize(%q) = %d, %v; want %d", tt.raw, got, err, tt.want)
			}
		})
	}
}

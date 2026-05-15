package loganalyzer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ArtifactStore struct {
	dir string
}

func NewArtifactStore(dir string) *ArtifactStore {
	return &ArtifactStore{dir: dir}
}

func (s *ArtifactStore) WriteJSON(prefix string, value any) (string, string, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", "", err
	}
	return s.WriteRaw(prefix, data)
}

func (s *ArtifactStore) WriteRaw(prefix string, data []byte) (string, string, error) {
	if s == nil || s.dir == "" {
		return "", "", nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", "", err
	}
	id := fmt.Sprintf("%s-%s", sanitizeArtifactPrefix(prefix), uuid.NewString())
	path := filepath.Join(s.dir, id+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", "", err
	}
	return id, path, nil
}

func (s *ArtifactStore) Sample(id string, maxLines int) (*ArtifactSampleResult, error) {
	if maxLines <= 0 {
		maxLines = 50
	}
	path, err := s.pathFor(id)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lines := make([]string, 0, maxLines)
	truncated := false
	for scanner.Scan() {
		if len(lines) >= maxLines {
			truncated = true
			break
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return &ArtifactSampleResult{ArtifactID: id, Path: path, Lines: lines, Truncated: truncated}, nil
}

func (s *ArtifactStore) ReadLogEntries(id string, maxEntries int) ([]LogEntry, error) {
	path, err := s.pathFor(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []LogEntry
	if err := json.Unmarshal(data, &entries); err == nil {
		return limitLogs(entries, maxEntries), nil
	}
	if parsed, err := parseLokiLogs(data); err == nil && len(parsed) > 0 {
		return limitLogs(parsed, maxEntries), nil
	}
	if _, parsed, err := parseOpenSearchLogs(data); err == nil && len(parsed) > 0 {
		return limitLogs(parsed, maxEntries), nil
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	parser := NewJSONParser()
	for scanner.Scan() {
		entry := parser.Parse(scanner.Text())
		if entry.Raw != "" {
			entries = append(entries, entry)
		}
		if maxEntries > 0 && len(entries) >= maxEntries {
			break
		}
	}
	return entries, scanner.Err()
}

func (s *ArtifactStore) ReadMetricResult(id string) (*MetricQueryResult, error) {
	path, err := s.pathFor(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result MetricQueryResult
	if err := json.Unmarshal(data, &result); err == nil && (result.Query != "" || len(result.Series) > 0) {
		return &result, nil
	}
	resultType, series, warnings, infos, err := parsePrometheusMetric(data)
	if err != nil {
		return nil, err
	}
	return &MetricQueryResult{ResultType: resultType, Series: series, Warnings: warnings, Infos: infos}, nil
}

func (s *ArtifactStore) ReadRaw(id string) ([]byte, error) {
	path, err := s.pathFor(id)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (s *ArtifactStore) Clean(ttl time.Duration, maxBytes int64) (*CleanArtifactsResult, error) {
	if s == nil || s.dir == "" {
		return &CleanArtifactsResult{}, nil
	}
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return &CleanArtifactsResult{}, nil
	}
	if err != nil {
		return nil, err
	}

	now := time.Now()
	type artifactInfo struct {
		path    string
		modTime time.Time
		size    int64
	}
	infos := []artifactInfo{}
	total := int64(0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		infos = append(infos, artifactInfo{path: path, modTime: info.ModTime(), size: info.Size()})
		total += info.Size()
	}

	removed := 0
	removedBytes := int64(0)
	for _, info := range infos {
		if ttl > 0 && now.Sub(info.modTime) > ttl {
			if os.Remove(info.path) == nil {
				removed++
				removedBytes += info.size
				total -= info.size
			}
		}
	}
	if maxBytes > 0 && total > maxBytes {
		sort.Slice(infos, func(i, j int) bool {
			return infos[i].modTime.Before(infos[j].modTime)
		})
		for _, info := range infos {
			if total <= maxBytes {
				break
			}
			if os.Remove(info.path) == nil {
				removed++
				removedBytes += info.size
				total -= info.size
			}
		}
	}
	return &CleanArtifactsResult{Removed: removed, Bytes: removedBytes}, nil
}

func (s *ArtifactStore) pathFor(id string) (string, error) {
	if s == nil || s.dir == "" {
		return "", errors.New("artifact directory is not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return "", errors.New("invalid artifact id")
	}
	return filepath.Join(s.dir, id+".json"), nil
}

func sanitizeArtifactPrefix(prefix string) string {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	prefix = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, prefix)
	prefix = strings.Trim(prefix, "-")
	if prefix == "" {
		return "artifact"
	}
	return prefix
}

func limitLogs(entries []LogEntry, maxEntries int) []LogEntry {
	if maxEntries > 0 && len(entries) > maxEntries {
		return entries[:maxEntries]
	}
	return entries
}

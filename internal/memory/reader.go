// Package memory implements RiskKernel's git-native memory layer: a user-owned
// directory of markdown/YAML/text files the agent reads. The files are yours —
// version them in git, edit them in your editor; RiskKernel only reads them.
//
// Retrieval is deterministic: list, read, and keyword search. There is NO
// embedding index / vector DB in v0.1 (CLAUDE.md §9) — semantic search is a future
// opt-in. Reads are path-traversal-safe: a request can never escape the root.
package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotFound is returned when a memory entry does not exist.
var ErrNotFound = errors.New("memory: not found")

// ErrUnsafePath is returned when a name would escape the memory root.
var ErrUnsafePath = errors.New("memory: unsafe path")

// Entry is a memory file's metadata.
type Entry struct {
	Namespace   string    `json:"namespace"`
	Name        string    `json:"name"` // path relative to the namespace
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Format      string    `json:"format"` // markdown | yaml | text
	Size        int64     `json:"size"`
	ModTime     time.Time `json:"modTime"`
}

// Reader reads a configured memory root directory.
type Reader struct {
	root string
}

// NewReader returns a Reader rooted at dir.
func NewReader(dir string) *Reader {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return &Reader{root: filepath.Clean(abs)}
}

// Root returns the configured memory root.
func (r *Reader) Root() string { return r.root }

var memoryExts = map[string]string{
	".md":       "markdown",
	".markdown": "markdown",
	".yaml":     "yaml",
	".yml":      "yaml",
	".txt":      "text",
}

// extOrder is the deterministic preference order for extension-less lookups (map
// iteration is randomized, so resolution must not depend on memoryExts ordering).
var extOrder = []string{".md", ".markdown", ".yaml", ".yml", ".txt"}

// List returns the memory entries under namespace (recursively). A missing
// directory yields an empty list, not an error.
func (r *Reader) List(namespace string) ([]Entry, error) {
	base, err := r.resolveDir(namespace)
	if err != nil {
		return nil, err
	}
	var out []Entry
	walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		format, ok := memoryExts[strings.ToLower(filepath.Ext(d.Name()))]
		if !ok {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		info, _ := d.Info()
		e := Entry{
			Namespace: namespace,
			Name:      filepath.ToSlash(rel),
			Format:    format,
			Size:      sizeOf(info),
			ModTime:   modTime(info),
		}
		e.Title, e.Description = metadata(path, e.Name, format)
		out = append(out, e)
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return nil, fmt.Errorf("memory: list %q: %w", namespace, walkErr)
	}
	return out, nil
}

// Read returns the content and metadata of a memory entry. The name may omit the
// file extension (e.g. "runbook" resolves to "runbook.md").
func (r *Reader) Read(namespace, name string) (string, Entry, error) {
	p, resolved, err := r.resolveReadable(namespace, name)
	if err != nil {
		return "", Entry{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", Entry{}, ErrNotFound
		}
		return "", Entry{}, fmt.Errorf("memory: read: %w", err)
	}
	format := memoryExts[strings.ToLower(filepath.Ext(p))]
	if format == "" {
		format = "text"
	}
	info, _ := os.Stat(p)
	e := Entry{
		Namespace: namespace, Name: filepath.ToSlash(resolved), Format: format,
		Size: sizeOf(info), ModTime: modTime(info),
	}
	e.Title, e.Description = titleFrom(string(data), resolved, format)
	return string(data), e, nil
}

// resolveReadable maps a (namespace, name) to an existing file path and the name
// that matched. It tries the literal name first; on a miss, and only if the name
// has no recognized memory extension, it tries name + each known extension (so
// "runbook" finds "runbook.md"). Path-traversal is rejected up front by
// resolveFile/safeJoin, before any extension fallback.
func (r *Reader) resolveReadable(namespace, name string) (string, string, error) {
	p, err := r.resolveFile(namespace, name)
	if err != nil {
		return "", "", err // e.g. path escapes the memory root — never fall through
	}
	if fi, statErr := os.Stat(p); statErr == nil && !fi.IsDir() {
		return p, name, nil
	}
	if _, known := memoryExts[strings.ToLower(filepath.Ext(name))]; known {
		return "", "", ErrNotFound // already had a known extension; don't stack another
	}
	for _, ext := range extOrder {
		cand := name + ext
		cp, err := r.resolveFile(namespace, cand)
		if err != nil {
			continue
		}
		if fi, statErr := os.Stat(cp); statErr == nil && !fi.IsDir() {
			return cp, cand, nil
		}
	}
	return "", "", ErrNotFound
}

// Search returns entries in a namespace whose title, name, or content contains
// the (case-insensitive) query. Deterministic keyword search — no embeddings.
func (r *Reader) Search(namespace, query string) ([]Entry, error) {
	entries, err := r.List(namespace)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return entries, nil
	}
	q := strings.ToLower(query)
	var out []Entry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), q) ||
			strings.Contains(strings.ToLower(e.Title), q) ||
			strings.Contains(strings.ToLower(e.Description), q) {
			out = append(out, e)
			continue
		}
		if content, _, err := r.Read(namespace, e.Name); err == nil &&
			strings.Contains(strings.ToLower(content), q) {
			out = append(out, e)
		}
	}
	return out, nil
}

// --- safe path resolution ---

func (r *Reader) resolveDir(namespace string) (string, error) {
	return r.safeJoin(namespace)
}

func (r *Reader) resolveFile(namespace, name string) (string, error) {
	return r.safeJoin(filepath.Join(namespace, name))
}

// safeJoin joins a user-supplied relative path under the root and guarantees the
// result cannot escape it. The cleaned path is validated with filepath.IsLocal —
// which rejects absolute paths, "..", and anything that would escape the base
// (and which static analysis recognizes as a path-traversal sanitizer) — backed
// by a prefix check as defense in depth.
func (r *Reader) safeJoin(rel string) (string, error) {
	clean := filepath.Clean(rel)
	if clean == "." { // the root itself (e.g. an empty namespace)
		return r.root, nil
	}
	if !filepath.IsLocal(clean) {
		return "", ErrUnsafePath
	}
	joined := filepath.Join(r.root, clean)
	if joined != r.root && !strings.HasPrefix(joined, r.root+string(os.PathSeparator)) {
		return "", ErrUnsafePath
	}
	return joined, nil
}

func sizeOf(info fs.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}

func modTime(info fs.FileInfo) time.Time {
	if info == nil {
		return time.Time{}
	}
	return info.ModTime()
}

// metadata reads just enough of a file to extract title/description.
func metadata(path, name, format string) (title, desc string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultTitle(name), ""
	}
	return titleFrom(string(data), name, format)
}

// titleFrom derives a title/description from a file's content. For markdown it
// reads simple `key: value` YAML frontmatter (no YAML dependency) and falls back
// to the first `# heading`; otherwise the filename.
func titleFrom(content, name, format string) (title, desc string) {
	title = defaultTitle(name)
	if format == "markdown" {
		fm := frontmatter(content)
		if t := fm["title"]; t != "" {
			title = t
		} else if n := fm["name"]; n != "" {
			title = n
		} else if h := firstHeading(content); h != "" {
			title = h
		}
		desc = fm["description"]
	}
	return title, desc
}

func defaultTitle(name string) string {
	base := filepath.Base(name)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// frontmatter parses a leading `---` … `---` block of simple key: value lines.
func frontmatter(content string) map[string]string {
	out := map[string]string{}
	if !strings.HasPrefix(content, "---") {
		return out
	}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return out
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(val), `"'`)
	}
	return out
}

func firstHeading(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(line[2:])
		}
	}
	return ""
}

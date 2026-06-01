package memory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestList_TitlesAndFormats(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "developer/style.md",
		"---\ntitle: Coding Style\ndescription: house rules\n---\n# ignored\nbody")
	writeFile(t, root, "developer/notes.md", "# Heading Title\n\nsome notes")
	writeFile(t, root, "developer/config.yaml", "key: value\n")
	writeFile(t, root, "developer/ignore.png", "binary") // non-memory ext skipped

	r := NewReader(root)
	entries, err := r.List("developer")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}
	byName := map[string]Entry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if byName["style.md"].Title != "Coding Style" || byName["style.md"].Description != "house rules" {
		t.Errorf("frontmatter title/desc = %+v", byName["style.md"])
	}
	if byName["style.md"].Format != "markdown" {
		t.Errorf("format = %q", byName["style.md"].Format)
	}
	if byName["notes.md"].Title != "Heading Title" {
		t.Errorf("heading title = %q", byName["notes.md"].Title)
	}
	if byName["config.yaml"].Format != "yaml" {
		t.Errorf("yaml format = %q", byName["config.yaml"].Format)
	}
}

func TestRead(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "ns/doc.md", "# Doc\ncontent here")
	r := NewReader(root)

	content, e, err := r.Read("ns", "doc.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if content != "# Doc\ncontent here" || e.Title != "Doc" {
		t.Fatalf("read = %q, entry=%+v", content, e)
	}

	if _, _, err := r.Read("ns", "missing.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRead_ExtensionlessName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "runbook.md", "# Deploy\nbuild, sign, push")
	r := NewReader(root)

	// "runbook" (no extension) resolves to "runbook.md".
	content, e, err := r.Read("", "runbook")
	if err != nil {
		t.Fatalf("extensionless Read: %v", err)
	}
	if content != "# Deploy\nbuild, sign, push" || e.Name != "runbook.md" || e.Format != "markdown" {
		t.Fatalf("resolved wrong: content=%q entry=%+v", content, e)
	}

	// A genuinely missing extension-less name is still ErrNotFound.
	if _, _, err := r.Read("", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSearch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "ns/a.md", "talks about postgres")
	writeFile(t, root, "ns/b.md", "talks about redis")
	r := NewReader(root)

	hits, err := r.Search("ns", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Name != "a.md" {
		t.Fatalf("search hits = %+v", hits)
	}
	// Empty query returns everything.
	all, _ := r.Search("ns", "")
	if len(all) != 2 {
		t.Fatalf("empty query = %d, want 2", len(all))
	}
}

func TestPathTraversalRejected(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "ns/ok.md", "ok")
	// A secret outside the root must be unreachable.
	writeFile(t, filepath.Dir(root), "secret.txt", "topsecret")

	r := NewReader(root)
	if _, _, err := r.Read("ns", "../../secret.txt"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("expected ErrUnsafePath, got %v", err)
	}
	if _, _, err := r.Read("..", "secret.txt"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("expected ErrUnsafePath for namespace escape, got %v", err)
	}
}

func TestList_MissingNamespaceIsEmpty(t *testing.T) {
	r := NewReader(t.TempDir())
	entries, err := r.List("does-not-exist")
	if err != nil {
		t.Fatalf("List on missing namespace should not error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty, got %+v", entries)
	}
}

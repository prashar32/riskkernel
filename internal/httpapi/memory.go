package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/prashar32/riskkernel/internal/httpx"
	"github.com/prashar32/riskkernel/internal/memory"
	"github.com/prashar32/riskkernel/internal/storage"
)

// handleListMemory implements GET /v1/memory?namespace=&q= — list (or keyword
// search) the user-owned markdown/YAML memory entries.
func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	q := r.URL.Query().Get("q")
	var (
		entries []memory.Entry
		err     error
	)
	if q != "" {
		entries, err = s.memory.Search(ns, q)
	} else {
		entries, err = s.memory.List(ns)
	}
	if err != nil {
		s.writeMemoryErr(w, err)
		return
	}
	if entries == nil {
		entries = []memory.Entry{}
	}
	httpx.WriteJSON(w, http.StatusOK, entries)
}

// handleReadMemory implements GET /v1/memory/entry?namespace=&name= — read one
// memory file's content + metadata.
func (s *Server) handleReadMemory(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	name := r.URL.Query().Get("name")
	if name == "" {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	content, e, err := s.memory.Read(ns, name)
	if err != nil {
		s.writeMemoryErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"namespace": e.Namespace, "name": e.Name, "title": e.Title,
		"description": e.Description, "format": e.Format,
		"size": e.Size, "modTime": e.ModTime, "content": content,
	})
}

// handleListFacts implements GET /v1/memory/facts?namespace= — episodic facts.
func (s *Server) handleListFacts(w http.ResponseWriter, r *http.Request) {
	store := s.runs.Store()
	if store == nil {
		httpx.WriteJSON(w, http.StatusOK, []any{})
		return
	}
	facts, err := store.ListFacts(r.Context(), r.URL.Query().Get("namespace"))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(facts))
	for _, f := range facts {
		out = append(out, factView(f))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type putFactBody struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	RunID     string `json:"runId"`
}

// handlePutFact implements PUT /v1/memory/facts — write an episodic fact.
func (s *Server) handlePutFact(w http.ResponseWriter, r *http.Request) {
	store := s.runs.Store()
	if store == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "no_store", "no durable store configured")
		return
	}
	var body putFactBody
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if body.Key == "" {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "key is required")
		return
	}
	f := storage.Fact{
		Namespace: body.Namespace, Key: body.Key, Value: body.Value,
		RunID: body.RunID, UpdatedAt: time.Now(),
	}
	if err := store.PutFact(r.Context(), f); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, factView(f))
}

func (s *Server) writeMemoryErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, memory.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "memory entry not found")
	case errors.Is(err, memory.ErrUnsafePath):
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "unsafe memory path")
	default:
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func factView(f storage.Fact) map[string]any {
	v := map[string]any{
		"namespace": f.Namespace, "key": f.Key, "value": f.Value,
		"updatedAt": f.UpdatedAt.Format(time.RFC3339),
	}
	if f.RunID != "" {
		v["runId"] = f.RunID
	}
	return v
}

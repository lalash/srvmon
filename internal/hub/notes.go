package hub

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

// maxNoteBytes bounds a single note. Long enough for a page of documentation
// about a server, short enough that the column cannot be used as storage.
const maxNoteBytes = 256 << 10

// excerptRadius is how much text is shown either side of a search match.
const excerptRadius = 90

func (h *Hub) handleGetNote(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid id"})
		return
	}
	note, err := h.store.GetNote(id)
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, apiError{Error: "server not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, note)
}

type noteInput struct {
	HTML string `json:"html"`
}

// handleSaveNote sanitizes on the way in, so what is stored is already safe to
// hand back. Sanitizing only on render would leave the raw markup one missed
// call away from executing.
func (h *Hub) handleSaveNote(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid id"})
		return
	}

	var in noteInput
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}
	if len(in.HTML) > maxNoteBytes {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "this note is too long"})
		return
	}

	clean := SanitizeNote(in.HTML)
	updatedAt, err := h.store.SetNote(id, clean)
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, apiError{Error: "server not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, Note{HTML: clean, UpdatedAt: updatedAt})
}

// handleSearchNotes matches against each note's plain text and returns an
// excerpt around the first hit, so the results say why they matched.
func (h *Hub) handleSearchNotes(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, http.StatusOK, map[string]any{"query": "", "hits": []NoteHit{}})
		return
	}

	candidates, bodies, err := h.store.NotesWithText()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}

	needle := strings.ToLower(query)
	hits := []NoteHit{}
	for _, candidate := range candidates {
		text := NoteText(bodies[candidate.ServerID])
		at := strings.Index(strings.ToLower(text), needle)
		if at < 0 {
			continue
		}
		candidate.Excerpt = excerpt(text, at, len(query))
		hits = append(hits, candidate)
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "hits": hits})
}

// excerpt cuts a window around a match, on rune boundaries so a multi-byte
// character is never split in half.
func excerpt(text string, at, length int) string {
	start := at - excerptRadius
	if start < 0 {
		start = 0
	}
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}

	end := at + length + excerptRadius
	if end > len(text) {
		end = len(text)
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}

	out := text[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out += "…"
	}
	return out
}

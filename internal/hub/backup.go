package hub

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// maxBackupBytes bounds an uploaded restore. A hub with years of history is
// nowhere near this; anything larger is a mistake or an attack.
const maxBackupBytes = 512 << 20

// handleBackup streams a consistent copy of the database. VACUUM INTO writes a
// clean snapshot without blocking writers, which a plain file copy cannot do
// while SQLite is mid-transaction.
func (h *Hub) handleBackup(w http.ResponseWriter, r *http.Request) {
	temp, err := os.MkdirTemp("", "srvmon-backup")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	defer os.RemoveAll(temp)

	snapshot := filepath.Join(temp, "srvmon.db")
	if err := h.store.BackupTo(snapshot); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "backup failed: " + err.Error()})
		return
	}

	file, err := os.Open(snapshot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}

	name := fmt.Sprintf("srvmon-backup-%s.db", time.Now().Format("20060102-1504"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	if _, err := io.Copy(w, file); err != nil {
		log.Printf("stream backup: %v", err)
	}
}

// handleRestore validates an uploaded database and swaps it in, then exits so
// the service manager starts a process holding the restored file.
//
// Replacing the file under the running process is not safe — open handles would
// still point at the old inode — and reopening in place would mean every holder
// of the *sql.DB had to be swapped atomically. Exiting is both simpler and
// verifiable: the unit restarts in about a second.
func (h *Hub) handleRestore(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "could not read the upload: " + err.Error()})
		return
	}

	upload, _, err := r.FormFile("backup")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "no file was uploaded"})
		return
	}
	defer upload.Close()

	incoming := h.cfg.DBPath + ".incoming"
	staged, err := os.OpenFile(incoming, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	if _, err := io.Copy(staged, upload); err != nil {
		staged.Close()
		os.Remove(incoming)
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	staged.Close()

	// Check it is a srvmon database before it is allowed anywhere near the live
	// one, so a wrong file leaves the running install untouched.
	summary, err := InspectBackup(incoming)
	if err != nil {
		os.Remove(incoming)
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}

	// The handle has to be closed before the file moves: Windows refuses to
	// rename an open file outright, and even where POSIX allows it the set-aside
	// copy would be missing whatever is still in the write-ahead log. Closing
	// checkpoints the WAL, so the rollback copy is a complete database.
	//
	// Every path from here ends in os.Exit — the store is gone either way, so
	// the process cannot go back to serving.
	if err := h.store.Close(); err != nil {
		log.Printf("close the database before restoring: %v", err)
	}

	rollback := h.cfg.DBPath + ".replaced"
	_ = os.Remove(rollback)
	if err := os.Rename(h.cfg.DBPath, rollback); err != nil {
		os.Remove(incoming)
		writeJSON(w, http.StatusInternalServerError,
			apiError{Error: "could not set the current database aside, restarting unchanged: " + err.Error()})
		exitAfterResponse(1)
		return
	}
	if err := os.Rename(incoming, h.cfg.DBPath); err != nil {
		_ = os.Rename(rollback, h.cfg.DBPath)
		writeJSON(w, http.StatusInternalServerError,
			apiError{Error: "could not install the backup, the previous database was put back: " + err.Error()})
		exitAfterResponse(1)
		return
	}
	// These belong to the database that just moved away.
	_ = os.Remove(h.cfg.DBPath + "-wal")
	_ = os.Remove(h.cfg.DBPath + "-shm")

	log.Printf("restored a backup with %d servers and %d history points, restarting",
		summary.Servers, summary.Samples)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"servers":  summary.Servers,
		"samples":  summary.Samples,
		"restarts": true,
	})
	exitAfterResponse(0)
}

// exitAfterResponse gives the reply time to reach the browser before the
// process goes; the service manager brings it back against the file on disk.
func exitAfterResponse(code int) {
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(code)
	}()
}

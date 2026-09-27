package observability

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ProblemLogFile is the file, inside the data root, that keeps recent
// warnings and errors across restarts.
const ProblemLogFile = "problems.jsonl"

// problemJournalKeep is how many warnings and errors survive a restart.
const problemJournalKeep = 200

// problemJournal appends warnings and errors to a JSON-lines file and trims it
// back to the newest problemJournalKeep entries once it holds twice that.
type problemJournal struct {
	mu    sync.Mutex
	path  string
	lines int
}

// Persist loads the warnings and errors kept from earlier runs into the
// buffer and keeps every new one in path. A missing file is not an error.
func (buffer *LogBuffer) Persist(path string) error {
	if buffer == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create problem log folder: %w", err)
	}
	entries, err := readProblemLog(path)
	if err != nil {
		return err
	}
	if len(entries) > problemJournalKeep {
		entries = entries[len(entries)-problemJournalKeep:]
	}
	if err := writeProblemLog(path, entries); err != nil {
		return err
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	current := buffer.entries
	buffer.entries = make([]LogEntry, 0, buffer.capacity)
	for _, entry := range append(entries, current...) {
		_ = buffer.push(entry)
	}
	buffer.journal = &problemJournal{path: path, lines: len(entries)}
	return nil
}

func (journal *problemJournal) append(entry LogEntry) {
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	file, err := os.OpenFile(journal.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, writeErr := file.Write(append(line, '\n'))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return
	}
	journal.lines++
	if journal.lines < 2*problemJournalKeep {
		return
	}
	entries, err := readProblemLog(journal.path)
	if err != nil {
		return
	}
	if len(entries) > problemJournalKeep {
		entries = entries[len(entries)-problemJournalKeep:]
	}
	if writeProblemLog(journal.path, entries) == nil {
		journal.lines = len(entries)
	}
}

func readProblemLog(path string) ([]LogEntry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read problem log: %w", err)
	}
	var entries []LogEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		var entry LogEntry
		// A torn last line from a crash is skipped rather than failing startup.
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Message != "" {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func writeProblemLog(path string, entries []LogEntry) error {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return fmt.Errorf("encode problem log: %w", err)
		}
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write problem log: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace problem log: %w", err)
	}
	return nil
}

// file: internal/database/pebble_store_activity.go
// version: 1.0.0
// guid: 2e007a48-ab98-4cd4-bd6a-f85b75de0cfa
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// AddSystemActivityLog stores a log entry from a housekeeping goroutine.
func (p *PebbleStore) AddSystemActivityLog(source, level, message string) error {
	key := fmt.Sprintf("syslog:%s:%s", time.Now().Format(time.RFC3339Nano), source)
	val := SystemActivityLog{
		Source:    source,
		Level:     level,
		Message:   message,
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(key), data, pebble.Sync)
}

// GetSystemActivityLogs retrieves recent system activity log entries.
func (p *PebbleStore) GetSystemActivityLogs(source string, limit int) ([]SystemActivityLog, error) {
	prefix := []byte("syslog:")
	upperBound := append(append([]byte{}, prefix...), 0xFF)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var logs []SystemActivityLog
	for iter.Last(); iter.Valid(); iter.Prev() {
		var l SystemActivityLog
		if err := json.Unmarshal(iter.Value(), &l); err != nil {
			continue
		}
		if source != "" && l.Source != source {
			continue
		}
		logs = append(logs, l)
		if len(logs) >= limit {
			break
		}
	}
	return logs, nil
}

// PruneOperationLogs deletes operation log entries older than the given time.
// Key format: operationlog:<operation_id>:<timestamp_nanos>:<seq>
func (p *PebbleStore) PruneOperationLogs(olderThan time.Time) (int, error) {
	prefix := "operationlog:"
	prefixBytes := []byte(prefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: append(append([]byte{}, prefixBytes...), 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	deleted := 0
	batch := p.db.NewBatch()
	defer batch.Close()

	olderThanNanos := olderThan.UnixNano()
	for iter.First(); iter.Valid(); iter.Next() {
		// Key: operationlog:<opID>:<nanos>:<seq>
		// Parse the JSON value to get CreatedAt.
		var logEntry OperationLog
		if jsonErr := json.Unmarshal(iter.Value(), &logEntry); jsonErr != nil {
			continue
		}
		if logEntry.CreatedAt.UnixNano() < olderThanNanos {
			if bErr := batch.Delete(iter.Key(), nil); bErr != nil {
				return 0, fmt.Errorf("pebble batch delete operationlog: %w", bErr)
			}
			deleted++
		}
	}
	if deleted > 0 {
		return deleted, batch.Commit(pebble.Sync)
	}
	return 0, nil
}

// PruneOperationChanges deletes operation change entries older than the given time.
// Key format: opchange:<operation_id>:<ulid>
func (p *PebbleStore) PruneOperationChanges(olderThan time.Time) (int, error) {
	prefix := "opchange:"
	prefixBytes := []byte(prefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: append(append([]byte{}, prefixBytes...), 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	deleted := 0
	batch := p.db.NewBatch()
	defer batch.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var change OperationChange
		if jsonErr := json.Unmarshal(iter.Value(), &change); jsonErr != nil {
			continue
		}
		if change.CreatedAt.Before(olderThan) {
			if bErr := batch.Delete(iter.Key(), nil); bErr != nil {
				return 0, fmt.Errorf("pebble batch delete opchange: %w", bErr)
			}
			deleted++
		}
	}
	if deleted > 0 {
		return deleted, batch.Commit(pebble.Sync)
	}
	return 0, nil
}

// PruneSystemActivityLogs deletes system activity log entries older than the given time.
// Key format: syslog:<RFC3339Nano>:<source>
func (p *PebbleStore) PruneSystemActivityLogs(olderThan time.Time) (int, error) {
	prefix := "syslog:"
	prefixBytes := []byte(prefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: append(append([]byte{}, prefixBytes...), 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	deleted := 0
	batch := p.db.NewBatch()
	defer batch.Close()

	// Key format: syslog:<RFC3339Nano>:<source>
	// RFC3339Nano contains colons (e.g. "2006-01-02T15:04:05.999999999Z07:00"),
	// so we parse the JSON value to get CreatedAt rather than parsing the key.
	for iter.First(); iter.Valid(); iter.Next() {
		var entry struct {
			CreatedAt time.Time `json:"created_at"`
		}
		if jsonErr := json.Unmarshal(iter.Value(), &entry); jsonErr != nil {
			continue
		}
		if entry.CreatedAt.Before(olderThan) {
			if bErr := batch.Delete(iter.Key(), nil); bErr != nil {
				return 0, fmt.Errorf("pebble batch delete syslog: %w", bErr)
			}
			deleted++
		}
	}
	if deleted > 0 {
		return deleted, batch.Commit(pebble.Sync)
	}
	return 0, nil
}

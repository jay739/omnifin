package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
)

var (
	backupRestoreMu    sync.Mutex
	errInvalidBackup   = errors.New("invalid database backup")
	errRestoreRollback = errors.New("could not put the original database back")
)

// openBackupArchive checks frame sizes before Badger allocates a decode buffer.
// A Badger backup is a sequence of length-prefixed protobuf lists, not a ZIP file.
func openBackupArchive(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	invalid := func() (*os.File, error) { _ = f.Close(); return nil, errInvalidBackup }
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !stat.Mode().IsRegular() || stat.Size() == 0 {
		return invalid()
	}
	remaining := stat.Size()
	var header [8]byte
	for remaining > 0 {
		if remaining < int64(len(header)) {
			return invalid()
		}
		if _, err := io.ReadFull(f, header[:]); err != nil {
			_ = f.Close()
			return nil, err
		}
		remaining -= int64(len(header))
		size := binary.LittleEndian.Uint64(header[:])
		if size == 0 || size > uint64(remaining) {
			return invalid()
		}
		if _, err := f.Seek(int64(size), io.SeekCurrent); err != nil {
			_ = f.Close()
			return nil, err
		}
		remaining -= int64(size)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func loadBackupArchive(db *badger.DB, source io.Reader) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: archive decoding failed: %v", errInvalidBackup, r)
		}
	}()
	if err := db.Load(source, 2); err != nil {
		return fmt.Errorf("%w: %v", errInvalidBackup, err)
	}
	return nil
}

// Decode against an isolated database. Validation never writes to the live store.
func validateBackupArchive(path string) error {
	source, err := openBackupArchive(path)
	if err != nil {
		return err
	}
	defer source.Close()
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		return err
	}
	loadErr := loadBackupArchive(db, source)
	closeErr := db.Close()
	if loadErr != nil {
		return loadErr
	}
	return closeErr
}

// Build and close the replacement before moving the original database. A failed
// decode or write therefore leaves the original database at its existing path.
func (app *appContext) restoreBackupDatabase(path string) error {
	source, err := openBackupArchive(path)
	if err != nil {
		return err
	}
	defer source.Close()
	staging, err := os.MkdirTemp(app.dataPath, "db-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	replacement := Storage{db_path: staging}
	if err := replacement.Connect(app.config); err != nil {
		return err
	}
	loadErr := loadBackupArchive(replacement.db.Badger(), source)
	closeErr := replacement.Close()
	if loadErr != nil {
		return loadErr
	}
	if closeErr != nil {
		return closeErr
	}

	previous := filepath.Join(app.dataPath, fmt.Sprintf("db-%d-pre-%s", time.Now().UnixNano(), filepath.Base(path)))
	if err := installBackupDatabase(staging, app.storage.db_path, previous); err != nil {
		return err
	}
	if _, err := os.Stat(previous); err == nil {
		app.info.Printf("Original database retained at %s", previous)
	}
	return nil
}

func installBackupDatabase(staging, current, previous string) error {
	if _, err := os.Stat(current); os.IsNotExist(err) {
		// A command-line restore may initialize a fresh installation.
		return os.Rename(staging, current)
	} else if err != nil {
		return err
	}
	if err := os.Rename(current, previous); err != nil {
		return err
	}
	if err := os.Rename(staging, current); err != nil {
		if rollbackErr := os.Rename(previous, current); rollbackErr != nil {
			return fmt.Errorf("%w: original is at %s; install error: %v; rollback error: %v", errRestoreRollback, previous, err, rollbackErr)
		}
		return err
	}
	return nil
}

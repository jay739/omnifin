package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jay739/omnifin/logger"
	"github.com/timshannon/badgerhold/v4"
	"gopkg.in/ini.v1"
)

type restoreFixtureRecord struct{ Value string }

func newBackupRestoreFixture(t *testing.T) *appContext {
	t.Helper()
	oldLoad, oldRestart, oldTray := LOADBAK, RESTART, TRAY
	LOADBAK, RESTART, TRAY = "", make(chan bool, 2), false
	t.Cleanup(func() { LOADBAK, RESTART, TRAY = oldLoad, oldRestart, oldTray })
	root := t.TempDir()
	cfg := &Config{File: ini.Empty()}
	cfg.Section("backups").Key("path").SetValue(filepath.Join(root, "backups"))
	if err := os.MkdirAll(cfg.Section("backups").Key("path").String(), 0700); err != nil {
		t.Fatal(err)
	}
	opts := badgerhold.DefaultOptions
	opts.Dir = filepath.Join(root, "db")
	opts.ValueDir, opts.Logger = opts.Dir, nil
	store, err := badgerhold.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Insert("marker", restoreFixtureRecord{Value: "original"}); err != nil {
		t.Fatal(err)
	}
	return &appContext{dataPath: root, config: cfg, storage: &Storage{db: store, db_path: opts.Dir}, LoggerSet: LoggerSet{info: logger.NewEmptyLogger(), debug: logger.NewEmptyLogger(), err: logger.NewEmptyLogger()}}
}

func uploadBackupFixture(t *testing.T, app *appContext, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("backups-file", "restore.bak")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/backups/restore", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	router := gin.New()
	router.POST("/backups/restore", app.RestoreBackup)
	router.ServeHTTP(response, request)
	return response
}

func TestBackupRestoreUploadWriteFailure(t *testing.T) {
	app := newBackupRestoreFixture(t)
	blocked := filepath.Join(app.dataPath, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	app.config.Section("backups").Key("path").SetValue(blocked)
	response := uploadBackupFixture(t, app, []byte("fixture"))
	if response.Code != http.StatusInternalServerError || LOADBAK != "" || len(RESTART) != 0 {
		t.Fatalf("failed write: status=%d pending=%q restarts=%d, want 500, no pending restore or restart", response.Code, LOADBAK, len(RESTART))
	}
	var marker restoreFixtureRecord
	if err := app.storage.db.Get("marker", &marker); err != nil || marker.Value != "original" {
		t.Fatalf("original database changed: %+v, %v", marker, err)
	}
}

func backupFixtureBytes(t *testing.T, value string) []byte {
	t.Helper()
	opts := badgerhold.DefaultOptions
	opts.Dir = t.TempDir()
	opts.ValueDir, opts.Logger = opts.Dir, nil
	store, err := badgerhold.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Insert("marker", restoreFixtureRecord{Value: value}); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if _, err := store.Badger().Backup(&data, 0); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func assertRestoreRejected(t *testing.T, app *appContext, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want || LOADBAK != "" || len(RESTART) != 0 {
		t.Fatalf("status=%d pending=%q restarts=%d, want %d and no restore: %s", response.Code, LOADBAK, len(RESTART), want, response.Body.String())
	}
	var marker restoreFixtureRecord
	if err := app.storage.db.Get("marker", &marker); err != nil || marker.Value != "original" {
		t.Fatalf("original changed: %+v, %v", marker, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(app.config.Section("backups").Key("path").String(), ".restore-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary uploads remain: %v, %v", leftovers, err)
	}
}

func restoreLocalFixture(app *appContext, name string) *httptest.ResponseRecorder {
	router := gin.New()
	router.POST("/backups/restore/:fname", app.RestoreLocalBackup)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/backups/restore/"+name, nil))
	return response
}

func TestBackupRestoreRejectsInvalidArchives(t *testing.T) {
	valid := backupFixtureBytes(t, "replacement")
	for name, data := range map[string][]byte{
		"empty": {}, "text": []byte("not a backup"),
		"truncated":                      valid[:len(valid)-1],
		"incomplete header":              {1, 2, 3},
		"impossible frame size":          {255, 255, 255, 255, 255, 255, 255, 255},
		"bad protobuf":                   {1, 0, 0, 0, 0, 0, 0, 0, 255},
		"corruption after valid records": append(bytes.Clone(valid), 1, 0, 0, 0, 0, 0, 0, 0, 255),
	} {
		t.Run(name, func(t *testing.T) {
			app := newBackupRestoreFixture(t)
			assertRestoreRejected(t, app, uploadBackupFixture(t, app, data), http.StatusBadRequest)
			files, err := os.ReadDir(app.config.Section("backups").Key("path").String())
			if err != nil || len(files) != 0 {
				t.Fatalf("invalid upload published: %v, %v", files, err)
			}
			fname := (Backup{}).String()
			path := filepath.Join(app.config.Section("backups").Key("path").String(), fname)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			assertRestoreRejected(t, app, restoreLocalFixture(app, fname), http.StatusBadRequest)
		})
	}
}

func TestBackupRestoreLocalMissingAndDirectory(t *testing.T) {
	app := newBackupRestoreFixture(t)
	fname := (Backup{}).String()
	assertRestoreRejected(t, app, restoreLocalFixture(app, fname), http.StatusNotFound)
	if err := os.Mkdir(filepath.Join(app.config.Section("backups").Key("path").String(), fname), 0700); err != nil {
		t.Fatal(err)
	}
	assertRestoreRejected(t, app, restoreLocalFixture(app, fname), http.StatusBadRequest)
	assertRestoreRejected(t, app, restoreLocalFixture(app, "not-a-backup.txt"), http.StatusBadRequest)
}

func TestBackupRestoreValidUploadAndPendingConflict(t *testing.T) {
	app := newBackupRestoreFixture(t)
	data := backupFixtureBytes(t, "replacement")
	response := uploadBackupFixture(t, app, data)
	if response.Code != http.StatusOK || LOADBAK == "" || len(RESTART) != 1 || !response.Flushed {
		t.Fatalf("accepted upload: status=%d path=%q restarts=%d flushed=%v", response.Code, LOADBAK, len(RESTART), response.Flushed)
	}
	pending := LOADBAK
	saved, err := os.ReadFile(pending)
	if err != nil || !bytes.Equal(saved, data) {
		t.Fatalf("saved backup differs: %v", err)
	}
	var marker restoreFixtureRecord
	if err := app.storage.db.Get("marker", &marker); err != nil || marker.Value != "original" {
		t.Fatalf("validation touched live database: %+v, %v", marker, err)
	}
	for _, response := range []*httptest.ResponseRecorder{uploadBackupFixture(t, app, data), restoreLocalFixture(app, filepath.Base(pending))} {
		if response.Code != http.StatusConflict || LOADBAK != pending || len(RESTART) != 1 {
			t.Fatalf("pending restore replaced: status=%d path=%q restarts=%d", response.Code, LOADBAK, len(RESTART))
		}
	}
	files, err := os.ReadDir(app.config.Section("backups").Key("path").String())
	if err != nil || len(files) != 1 {
		t.Fatalf("unexpected upload files: %v, %v", files, err)
	}
}

func TestBackupRestoreValidLocal(t *testing.T) {
	app := newBackupRestoreFixture(t)
	path := filepath.Join(app.config.Section("backups").Key("path").String(), (Backup{}).String())
	if err := os.WriteFile(path, backupFixtureBytes(t, "replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	response := restoreLocalFixture(app, filepath.Base(path))
	if response.Code != http.StatusOK || LOADBAK != path || len(RESTART) != 1 || !response.Flushed {
		t.Fatalf("local restore not accepted: %d %q %d", response.Code, LOADBAK, len(RESTART))
	}
}

func openRestoreMarker(t *testing.T, path, want string) {
	t.Helper()
	opts := badgerhold.DefaultOptions
	opts.Dir, opts.ValueDir, opts.Logger = path, path, nil
	store, err := badgerhold.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var marker restoreFixtureRecord
	if err := store.Get("marker", &marker); err != nil || marker.Value != want {
		t.Fatalf("marker at %s = %+v, %v; want %q", path, marker, err, want)
	}
}

func TestBackupRestoreStartupKeepsOriginalOnFailure(t *testing.T) {
	for _, state := range []string{"missing", "corrupt", "invalid protobuf", "replacement unavailable"} {
		t.Run(state, func(t *testing.T) {
			app := newBackupRestoreFixture(t)
			if err := app.storage.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(app.config.Section("backups").Key("path").String(), (Backup{}).String())
			if state != "missing" {
				data := []byte("bad backup")
				if state == "invalid protobuf" {
					data = []byte{1, 0, 0, 0, 0, 0, 0, 0, 255}
				}
				if state == "replacement unavailable" {
					data = backupFixtureBytes(t, "replacement")
					app.config.Section("advanced").Key("value_log_size").SetValue("0")
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			LOADBAK = path
			app.loadPendingBackup()
			if LOADBAK != "" {
				t.Fatalf("failed restore remains pending: %s", LOADBAK)
			}
			openRestoreMarker(t, app.storage.db_path, "original")
			archives, _ := filepath.Glob(filepath.Join(app.dataPath, "db-*-pre-*"))
			staging, _ := filepath.Glob(filepath.Join(app.dataPath, "db-restore-*"))
			if len(archives) != 0 || len(staging) != 0 {
				t.Fatalf("failed restore moved database or left staging: %v %v", archives, staging)
			}
		})
	}
}

func TestBackupRestoreStartupReplacesOnlyAfterCompleteLoad(t *testing.T) {
	app := newBackupRestoreFixture(t)
	if err := app.storage.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(app.config.Section("backups").Key("path").String(), (Backup{}).String())
	if err := os.WriteFile(path, backupFixtureBytes(t, "replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	LOADBAK = path
	app.loadPendingBackup()
	if LOADBAK != "" {
		t.Fatal("successful restore remains pending")
	}
	openRestoreMarker(t, app.storage.db_path, "replacement")
	archives, err := filepath.Glob(filepath.Join(app.dataPath, "db-*-pre-*"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("original not retained: %v, %v", archives, err)
	}
	openRestoreMarker(t, archives[0], "original")
	staging, _ := filepath.Glob(filepath.Join(app.dataPath, "db-restore-*"))
	if len(staging) != 0 {
		t.Fatalf("staging directory remains: %v", staging)
	}
}

func TestBackupRestoreAcknowledgesBeforeRestartSignal(t *testing.T) {
	app := newBackupRestoreFixture(t)
	RESTART = make(chan bool)
	path := filepath.Join(app.config.Section("backups").Key("path").String(), (Backup{}).String())
	if err := os.WriteFile(path, backupFixtureBytes(t, "replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/backups/restore/:fname", app.RestoreLocalBackup)
	server := httptest.NewServer(router)
	defer server.Close()
	// Keep Restart blocked until the client has received the complete response.
	defer func() {
		select {
		case <-RESTART:
		case <-time.After(3 * time.Second):
			t.Error("restart was not signaled")
		}
	}()
	client := server.Client()
	client.Timeout = 2 * time.Second
	response, err := client.Post(server.URL+"/backups/restore/"+filepath.Base(path), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || !bytes.Contains(body, []byte(`"success":true`)) {
		t.Fatalf("restore was not fully acknowledged before restart: status=%d body=%s err=%v", response.StatusCode, body, err)
	}
}

func TestBackupRestoreInitializesFreshDatabase(t *testing.T) {
	app := newBackupRestoreFixture(t)
	if err := app.storage.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(app.storage.db_path); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(app.config.Section("backups").Key("path").String(), (Backup{}).String())
	if err := os.WriteFile(path, backupFixtureBytes(t, "replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	LOADBAK = path
	app.loadPendingBackup()
	openRestoreMarker(t, app.storage.db_path, "replacement")
}

func TestBackupRestoreRollsBackFailedInstall(t *testing.T) {
	app := newBackupRestoreFixture(t)
	if err := app.storage.Close(); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(app.dataPath, "previous")
	// A missing replacement forces the second move to fail after moving the original.
	err := installBackupDatabase(filepath.Join(app.dataPath, "missing-replacement"), app.storage.db_path, previous)
	if err == nil {
		t.Fatal("missing replacement was accepted")
	}
	openRestoreMarker(t, app.storage.db_path, "original")
	if _, err := os.Stat(previous); !os.IsNotExist(err) {
		t.Fatalf("original was not moved back: %v", err)
	}
}

func TestBackupRestoreConcurrentRequests(t *testing.T) {
	app := newBackupRestoreFixture(t)
	data := backupFixtureBytes(t, "replacement")
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { responses <- uploadBackupFixture(t, app, data) }()
	}
	statuses := map[int]int{}
	for range 2 {
		statuses[(<-responses).Code]++
	}
	if statuses[200] != 1 || statuses[409] != 1 || len(RESTART) != 1 {
		t.Fatalf("concurrent restores: statuses=%v restarts=%d", statuses, len(RESTART))
	}
	saved, err := os.ReadFile(LOADBAK)
	if err != nil || !bytes.Equal(saved, data) {
		t.Fatalf("pending archive changed: %v", err)
	}
}

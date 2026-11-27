package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"
	lm "github.com/jay739/omnifin/logmessages"
)

// @Summary Creates a backup of the database.
// @Router /backups [post]
// @Success 200 {object} CreateBackupDTO
// @Security Bearer
// @tags Backups
func (app *appContext) CreateBackup(gc *gin.Context) {
	backup := app.makeBackup()
	gc.JSON(200, backup)
}

// @Summary Download a specific backup file. Requires auth, so can't be accessed plainly in the browser.
// @Param fname path string true "backup filename"
// @Router /backups/{fname} [get]
// @Produce octet-stream
// @Produce json
// @Success 200 {body} file
// @Failure 400 {object} boolResponse
// @Security Bearer
// @tags Backups
func (app *appContext) GetBackup(gc *gin.Context) {
	escapedFName := gc.Param("fname")
	fname, err := url.QueryUnescape(escapedFName)
	if err != nil {
		respondBool(400, false, gc)
		return
	}
	// Defence in depth against path traversal: strip any directory components
	// before we ever join against the backups dir. Belt-and-suspenders alongside
	// Backup.FromString's format check below.
	fname = filepath.Base(fname)
	b := Backup{}
	err = b.FromString(fname)
	if err != nil || b.Date.IsZero() {
		app.debug.Printf(lm.IgnoreInvalidFilename, fname, err)
		respondBool(400, false, gc)
		return
	}
	path := app.config.Section("backups").Key("path").String()
	fullpath := filepath.Join(path, fname)
	gc.FileAttachment(fullpath, fname)
}

// @Summary Get a list of backups.
// @Router /backups [get]
// @Produce json
// @Success 200 {object} GetBackupsDTO
// @Security Bearer
// @tags Backups
func (app *appContext) GetBackups(gc *gin.Context) {
	path := app.config.Section("backups").Key("path").String()
	backups := app.getBackups()
	sort.Sort(backups)
	resp := GetBackupsDTO{}
	resp.Backups = make([]CreateBackupDTO, backups.count)

	for i, item := range backups.files[:backups.count] {
		resp.Backups[i].Name = item.Name()
		fullpath := filepath.Join(path, item.Name())
		resp.Backups[i].Path = fullpath
		resp.Backups[i].Date = backups.info[i].Date.Unix()
		resp.Backups[i].Commit = backups.info[i].Commit
		fstat, err := os.Stat(fullpath)
		if err == nil {
			resp.Backups[i].Size = fileSize(fstat.Size())
		}
	}
	gc.JSON(200, resp)
}

// @Summary Restore a validated backup file stored locally to the server.
// @Param fname path string true "backup filename"
// @Router /backups/restore/{fname} [post]
// @Produce json
// @Success 200 {object} boolResponse
// @Failure 400 {object} stringResponse
// @Failure 404 {object} stringResponse
// @Failure 409 {object} stringResponse
// @Failure 500 {object} stringResponse
// @Security Bearer
// @tags Backups
func (app *appContext) RestoreLocalBackup(gc *gin.Context) {
	backupRestoreMu.Lock()
	defer backupRestoreMu.Unlock()
	if LOADBAK != "" {
		respond(409, "A backup restore is already pending. Wait for Omnifin to restart.", gc)
		return
	}
	fname := filepath.Base(gc.Param("fname"))
	b := Backup{}
	if err := b.FromString(fname); err != nil || b.Date.IsZero() {
		respond(400, "Choose a valid Omnifin database backup filename.", gc)
		return
	}
	fullpath := filepath.Join(app.config.Section("backups").Key("path").String(), fname)
	if err := validateBackupArchive(fullpath); err != nil {
		app.backupRestoreError(gc, err)
		return
	}
	app.queueBackupRestore(gc, fullpath)
}

// @Summary Restore a backup uploaded by the user after saving and validating it.
// @Param backups-file formData file true "Badger database .bak file, up to 100 MB"
// @Router /backups/restore [post]
// @Produce json
// @Success 200 {object} boolResponse
// @Failure 400 {object} stringResponse
// @Failure 409 {object} stringResponse
// @Failure 500 {object} stringResponse
// @Security Bearer
// @tags Backups
func (app *appContext) RestoreBackup(gc *gin.Context) {
	backupRestoreMu.Lock()
	defer backupRestoreMu.Unlock()
	if LOADBAK != "" {
		respond(409, "A backup restore is already pending. Wait for Omnifin to restart.", gc)
		return
	}
	const maxBackupBytes = 100 << 20 // 100 MB including multipart framing.
	gc.Request.Body = http.MaxBytesReader(gc.Writer, gc.Request.Body, maxBackupBytes)
	file, err := gc.FormFile("backups-file")
	if gc.Request.MultipartForm != nil {
		defer gc.Request.MultipartForm.RemoveAll()
	}
	if err != nil || file.Size > maxBackupBytes {
		respond(400, "Choose a database backup smaller than 100 MB and upload it again.", gc)
		return
	}
	path := app.config.Section("backups").Key("path").String()
	if err := os.MkdirAll(path, 0700); err != nil {
		app.backupRestoreError(gc, err)
		return
	}
	saved, err := os.CreateTemp(path, ".restore-*")
	if err != nil {
		app.backupRestoreError(gc, err)
		return
	}
	defer os.Remove(saved.Name())
	source, err := file.Open()
	if err != nil {
		_ = saved.Close()
		app.backupRestoreError(gc, err)
		return
	}
	_, copyErr := io.Copy(saved, source)
	sourceErr := source.Close()
	syncErr := saved.Sync()
	closeErr := saved.Close()
	if err := errors.Join(copyErr, sourceErr, syncErr, closeErr); err != nil {
		app.backupRestoreError(gc, err)
		return
	}
	if err := validateBackupArchive(saved.Name()); err != nil {
		app.backupRestoreError(gc, err)
		return
	}
	fullpath := filepath.Join(path, (Backup{Upload: true}).String())
	// Restore requests are serialized, and a new upload must not replace an archive.
	if _, err := os.Lstat(fullpath); err == nil {
		respond(409, "An uploaded backup already has this timestamp. Wait a moment and upload again.", gc)
		return
	} else if !os.IsNotExist(err) {
		app.backupRestoreError(gc, err)
		return
	}
	if err := os.Rename(saved.Name(), fullpath); err != nil {
		app.backupRestoreError(gc, err)
		return
	}
	app.queueBackupRestore(gc, fullpath)
}

func (app *appContext) backupRestoreError(gc *gin.Context, err error) {
	app.err.Printf("Could not prepare database restore: %v", err)
	switch {
	case errors.Is(err, errInvalidBackup):
		respond(400, "This file is not a complete, readable database backup. Choose another .bak file.", gc)
	case os.IsNotExist(err):
		respond(404, "The backup file no longer exists. Refresh the backup list and choose another file.", gc)
	default:
		respond(500, "Could not save or read the backup. Check the backup directory and available disk space, then retry.", gc)
	}
}

func (app *appContext) queueBackupRestore(gc *gin.Context, path string) {
	LOADBAK = path
	// Acknowledge acceptance before the restart closes active HTTP connections.
	body, _ := json.Marshal(boolResponse{Success: true})
	gc.Header("Content-Length", strconv.Itoa(len(body)))
	gc.Data(200, "application/json; charset=utf-8", body)
	gc.Abort()
	gc.Writer.Flush()
	app.restart(gc)
}

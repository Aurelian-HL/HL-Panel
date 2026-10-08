package httpapi

import (
	"io"
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/migrationbackup"
)

func (api *API) exportMigration(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input struct {
		AdministratorPassword string `json:"administrator_password"`
		Password              string `json:"password"`
		SourceURL             string `json:"source_url"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	raw, err := api.migration.Export(r.Context(), session.AdminID, input.AdministratorPassword, input.Password, input.SourceURL)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeMigrationFile(w, raw, "hl-panel-migration.hlbackup")
}
func writeMigrationFile(w http.ResponseWriter, raw []byte, name string) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
func (api *API) inspectMigration(w http.ResponseWriter, r *http.Request, session auth.Session) {
	api.uploadMigration(w, r, session, false)
}
func (api *API) importMigration(w http.ResponseWriter, r *http.Request, session auth.Session) {
	api.uploadMigration(w, r, session, true)
}
func (api *API) uploadMigration(w http.ResponseWriter, r *http.Request, session auth.Session, restore bool) {
	r.Body = http.MaxBytesReader(w, r.Body, migrationbackup.MaxArchiveBytes+65536)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeJSON(w, 400, map[string]string{"message": "请上传不超过 64 MB 的迁移备份包"})
		return
	}
	defer r.MultipartForm.RemoveAll()
	if len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["file"]) != 1 {
		writeJSON(w, 400, map[string]string{"message": "每次只能上传一个迁移备份包"})
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, 400, map[string]string{"message": "请选择迁移备份包"})
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, migrationbackup.MaxArchiveBytes+1))
	if err != nil || len(raw) > migrationbackup.MaxArchiveBytes {
		writeJSON(w, 400, map[string]string{"message": "备份包读取失败或超过 64 MB"})
		return
	}
	if !restore {
		preview, e := api.migration.Preview(r.Context(), session.AdminID, r.FormValue("administrator_password"), r.FormValue("password"), raw)
		if e != nil {
			writeProblem(w, r, e)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, preview)
		return
	}
	if r.FormValue("confirm") != "RESTORE" {
		writeJSON(w, 400, map[string]string{"message": "请先核对导入预览并确认恢复"})
		return
	}
	result, err := api.migration.Restore(r.Context(), session.AdminID, r.FormValue("administrator_password"), r.FormValue("password"), r.Header.Get("Idempotency-Key"), r.FormValue("digest"), r.FormValue("target_url"), raw)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}
func (api *API) migrationRecovery(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	raw, err := api.migration.Recovery(r.PathValue("backup_id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeMigrationFile(w, raw, "hl-panel-before-import.hlbackup")
}

package httpfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/infinimesh/proto/node/access"
)

type Access interface {
	Level(ctx context.Context, account, namespace string) (access.Level, error)
}

type Handler struct {
	store  Store
	access Access
	key    []byte
	cfg    Config
	mux    *http.ServeMux
}

func New(store Store, access Access, signingKey []byte, cfg Config) *Handler {
	h := &Handler{store: store, access: access, key: signingKey, cfg: cfg, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h.mux.HandleFunc("GET /{namespace}", h.list)
	h.mux.HandleFunc("GET /{namespace}/{name}", h.get)
	h.mux.HandleFunc("POST /{namespace}/{name}", h.upload)
	h.mux.HandleFunc("DELETE /{namespace}/{name}", h.delete)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	if _, ok := h.permit(w, r, ns, access.Level_READ); !ok {
		return
	}
	files, err := h.store.List(r.Context(), ns)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	body := struct {
		Files     []fileJSON `json:"files"`
		FileLimit int64      `json:"file_limit"`
	}{Files: make([]fileJSON, 0, len(files)), FileLimit: h.cfg.MaxBytes}
	for _, f := range files {
		body.Files = append(body.Files, fileJSON{Name: f.Name, Size: f.Size, ModTime: f.ModTime.UTC()})
	}
	writeJSON(w, http.StatusOK, body)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	obj, err := h.store.Get(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	defer obj.Body.Close()
	if obj.ContentType != "" {
		w.Header().Set("Content-Type", obj.ContentType)
	}
	http.ServeContent(w, r, obj.Name, obj.ModTime, readSeeker(obj.Body, obj.Size))
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")
	if _, ok := h.permit(w, r, ns, access.Level_MGMT); !ok {
		return
	}
	if err := safeSegment(ns); err != nil || safeSegment(name) != nil {
		writeErr(w, http.StatusBadRequest, "invalid name")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBytes+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) || strings.Contains(err.Error(), "too large") {
			writeErr(w, http.StatusRequestEntityTooLarge, "file is too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "file is required")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(file, h.cfg.MaxBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file is required")
		return
	}
	if n > h.cfg.MaxBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "file is too large")
		return
	}
	ctype := hdr.Header.Get("Content-Type")
	if ctype == "" || strings.HasPrefix(strings.ToLower(ctype), "application/octet-stream") {
		ctype = http.DetectContentType(buf.Bytes())
	}
	media, _, _ := mime.ParseMediaType(ctype)
	if media == "" {
		media = ctype
	}
	if !h.cfg.allows(media) {
		writeErr(w, http.StatusUnsupportedMediaType, "file type is not allowed")
		return
	}
	if err := h.store.Put(r.Context(), ns, name, bytes.NewReader(buf.Bytes()), n, media); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	if _, ok := h.permit(w, r, ns, access.Level_MGMT); !ok {
		return
	}
	if err := h.store.Delete(r.Context(), ns, r.PathValue("name")); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "deleted"})
}

func (h *Handler) permit(w http.ResponseWriter, r *http.Request, namespace string, need access.Level) (string, bool) {
	account, err := accountFromBearer(h.key, r.Header.Get("Authorization"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	level, err := h.access.Level(r.Context(), account, namespace)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "access check failed")
		return "", false
	}
	if level < need {
		writeErr(w, http.StatusForbidden, "forbidden")
		return "", false
	}
	return account, true
}

func accountFromBearer(key []byte, header string) (string, error) {
	raw, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || strings.TrimSpace(raw) == "" {
		return "", errors.New("unauthorized")
	}
	token, err := jwt.Parse(raw, func(t *jwt.Token) (interface{}, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("unexpected signing method")
		}
		return key, nil
	})
	if err != nil || !token.Valid {
		return "", errors.New("unauthorized")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("unauthorized")
	}
	account, _ := claims["account"].(string)
	if account == "" {
		return "", errors.New("unauthorized")
	}
	return account, nil
}

type fileJSON struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"message": msg})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotFound) || err.Error() == "invalid name" {
		if err.Error() == "invalid name" {
			writeErr(w, http.StatusBadRequest, "invalid name")
			return
		}
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeErr(w, http.StatusInternalServerError, "storage failed")
}

type bytesReadSeeker struct {
	*bytes.Reader
}

func readSeeker(body io.ReadCloser, size int64) io.ReadSeeker {
	if rs, ok := body.(io.ReadSeeker); ok {
		return rs
	}
	buf, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return bytes.NewReader(nil)
	}
	return bytes.NewReader(buf)
}

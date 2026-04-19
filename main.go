package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	srvReadTimeout       = 5 * time.Second
	srvReadHeaderTimeout = 1 * time.Second
	srvWriteTimeout      = 5 * time.Second
	srvIdleTimeout       = 30 * time.Second
	srvShutdownTimeout   = 5 * time.Second
	srvMaxHeaderBytes    = 16 * 1024  // 16kb
	srvMaxBodyBytes      = 128 * 1024 // 128kb

	headerEchoHost   = "X-Nginx-Echo-Host"
	headerEchoIP     = "X-Nginx-Echo-Ip"
	headerEchoScheme = "X-Nginx-Echo-Scheme"
)

type response struct {
	IP      string         `json:"ip"`
	Method  string         `json:"method"`
	URL     string         `json:"url"`
	Headers map[string]any `json:"headers"`
	Params  map[string]any `json:"params,omitempty"`
	Data    string         `json:"data,omitempty"`
	JSON    any            `json:"json,omitempty"`
}

type responseError struct {
	Code   int    `json:"code"`
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

func configureHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleEcho)
	return limitRequestSize(mux)
}

func limitRequestSize(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, srvMaxBodyBytes)
		}
		h.ServeHTTP(w, r)
	})
}

func flatten(m map[string][]string, skip func(string) bool) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if skip != nil && skip(k) {
			continue
		}
		if len(v) == 1 {
			out[k] = v[0]
		} else {
			out[k] = v
		}
	}
	return out
}

func cleanHeaders(headers http.Header) map[string]any {
	return flatten(headers, func(k string) bool {
		return k == headerEchoHost || k == headerEchoIP || k == headerEchoScheme
	})
}

func encodeData(body []byte, contentType string) string {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	data := base64.URLEncoding.EncodeToString(body)
	return "data:" + contentType + ";base64," + data
}

func parseBody(r *http.Request, resp *response) error {
	defer r.Body.Close()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}

	if len(body) == 0 {
		return nil
	}

	contentType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	contentType = strings.ToLower(strings.TrimSpace(contentType))

	switch contentType {
	case "text/html", "text/plain":
		return nil

	case "application/x-www-form-urlencoded":
		if _, err := url.ParseQuery(string(body)); err != nil {
			return err
		}
		resp.Data = string(body)

	case "application/json":
		if err := json.Unmarshal(body, &resp.JSON); err != nil {
			return err
		}

	default:
		resp.Data = encodeData(body, contentType)
	}

	return nil
}

func getHost(r *http.Request) string {
	return r.Header.Get(headerEchoHost)
}

func getHeaders(r *http.Request) http.Header {
	headers := r.Header.Clone()
	headers.Set("Host", getHost(r))
	if len(r.TransferEncoding) > 0 {
		headers.Set("Transfer-Encoding", strings.Join(r.TransferEncoding, ","))
	}
	return headers
}

func getIP(r *http.Request) string {
	return r.Header.Get(headerEchoIP)
}

func getScheme(r *http.Request) string {
	return r.Header.Get(headerEchoScheme)
}

func getParams(r *http.Request) map[string]any {
	return flatten(r.URL.Query(), nil)
}

func getURL(r *http.Request) string {
	u := *r.URL
	u.Scheme = getScheme(r)
	u.Host = getHost(r)
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String()
}

func writeJSON(w http.ResponseWriter, status int, val any) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(val); err != nil {
		log.Printf("echo: json encode error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		log.Printf("echo: response write error: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, err error) {
	resp := responseError{
		Code:  code,
		Error: http.StatusText(code),
	}
	if err != nil {
		resp.Detail = err.Error()
	}
	writeJSON(w, code, resp)
}

func handleEcho(w http.ResponseWriter, r *http.Request) {
	resp := &response{
		Headers: cleanHeaders(getHeaders(r)),
		Method:  r.Method,
		IP:      getIP(r),
		URL:     getURL(r),
	}

	if params := getParams(r); len(params) > 0 {
		resp.Params = params
	}

	switch r.Method {
	case http.MethodDelete, http.MethodPatch, http.MethodPost, http.MethodPut:
		if err := parseBody(r, resp); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				writeError(w, http.StatusRequestEntityTooLarge, err)
				return
			}
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func main() {
	srv := &http.Server{
		Addr:              "127.0.0.1:7777",
		Handler:           configureHandler(),
		MaxHeaderBytes:    srvMaxHeaderBytes,
		ReadHeaderTimeout: srvReadHeaderTimeout,
		ReadTimeout:       srvReadTimeout,
		WriteTimeout:      srvWriteTimeout,
		IdleTimeout:       srvIdleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("echo: listen error: %v", err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), srvShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("echo: shutdown error: %v", err)
	}
}

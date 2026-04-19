package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(configureHandler())
	t.Cleanup(srv.Close)
	return srv
}

func doRequest(
	t *testing.T,
	srv *httptest.Server,
	method, path string,
	body io.Reader,
	headers map[string]string,
) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, b
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode json: %v (body: %s)", err, b)
	}
	return m
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T: %v", v, v)
	}
	return m
}

func asFloat(t *testing.T, v any) float64 {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("expected float64, got %T: %v", v, v)
	}
	return f
}

func asString(t *testing.T, v any) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("expected string, got %T: %v", v, v)
	}
	return s
}

func TestGETWithEchoHeadersAndParams(t *testing.T) {
	srv := newTestServer(t)
	resp, body := doRequest(
		t,
		srv,
		http.MethodGet,
		"/path/here?a=1&b=2&b=3",
		nil,
		map[string]string{
			headerEchoHost:   "example.com",
			headerEchoIP:     "1.2.3.4",
			headerEchoScheme: "https",
		},
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	m := decode(t, body)

	if got := m["ip"]; got != "1.2.3.4" {
		t.Errorf("ip = %v", got)
	}
	if got := m["method"]; got != "GET" {
		t.Errorf("method = %v", got)
	}
	if got := m["url"]; got != "https://example.com/path/here?a=1&b=2&b=3" {
		t.Errorf("url = %v", got)
	}

	params := asMap(t, m["params"])
	if got := params["a"]; got != "1" {
		t.Errorf("params.a = %v", got)
	}
	b, ok := params["b"].([]any)
	if !ok || len(b) != 2 || b[0] != "2" || b[1] != "3" {
		t.Errorf("params.b = %v", params["b"])
	}

	headers := asMap(t, m["headers"])
	for _, k := range []string{headerEchoHost, headerEchoIP, headerEchoScheme} {
		if _, ok := headers[k]; ok {
			t.Errorf("echo header %q leaked into response headers", k)
		}
	}
	if headers["Host"] != "example.com" {
		t.Errorf("Host = %v, want example.com", headers["Host"])
	}
}

func TestGETMultiValuedHeader(t *testing.T) {
	srv := newTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Add("X-Test", "one")
	req.Header.Add("X-Test", "two")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	m := decode(t, body)
	headers := asMap(t, m["headers"])
	vals, ok := headers["X-Test"].([]any)
	if !ok || len(vals) != 2 || vals[0] != "one" || vals[1] != "two" {
		t.Errorf("X-Test = %v", headers["X-Test"])
	}
}

func TestHEAD(t *testing.T) {
	srv := newTestServer(t)
	resp, body := doRequest(t, srv, http.MethodHead, "/?x=1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("HEAD body should be empty, got %d bytes", len(body))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
}

func TestPOSTJSON(t *testing.T) {
	srv := newTestServer(t)
	resp, body := doRequest(
		t,
		srv,
		http.MethodPost,
		"/",
		strings.NewReader(`{"hello":"world","n":42}`),
		map[string]string{
			"Content-Type": "application/json",
		},
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	m := decode(t, body)
	j, ok := m["json"].(map[string]any)
	if !ok {
		t.Fatalf("json field missing or wrong type: %v", m["json"])
	}
	if j["hello"] != "world" || asFloat(t, j["n"]) != 42 {
		t.Errorf("decoded json = %v", j)
	}
}

func TestPOSTJSONMixedCaseContentType(t *testing.T) {
	srv := newTestServer(t)
	resp, body := doRequest(
		t,
		srv,
		http.MethodPost,
		"/",
		strings.NewReader(`{"k":1}`),
		map[string]string{
			"Content-Type": "Application/JSON; charset=utf-8",
		},
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	m := decode(t, body)
	j, ok := m["json"].(map[string]any)
	if !ok || asFloat(t, j["k"]) != 1 {
		t.Errorf("json = %v", m["json"])
	}
}

func TestPOSTForm(t *testing.T) {
	srv := newTestServer(t)
	resp, body := doRequest(
		t,
		srv,
		http.MethodPost,
		"/",
		strings.NewReader("a=1&b=two&a=also"),
		map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		},
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	m := decode(t, body)
	if m["data"] != "a=1&b=two&a=also" {
		t.Errorf("data = %v", m["data"])
	}
	if _, ok := m["json"]; ok {
		t.Errorf("unexpected json field")
	}
}

func TestPOSTTextPlainPassthrough(t *testing.T) {
	srv := newTestServer(t)
	resp, body := doRequest(
		t,
		srv,
		http.MethodPost,
		"/",
		strings.NewReader("hello plain"),
		map[string]string{
			"Content-Type": "text/plain",
		},
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	m := decode(t, body)
	if _, ok := m["data"]; ok {
		t.Errorf("text/plain should not populate data, got %v", m["data"])
	}
	if _, ok := m["json"]; ok {
		t.Errorf("text/plain should not populate json")
	}
}

func TestPOSTBinaryBase64(t *testing.T) {
	srv := newTestServer(t)
	resp, body := doRequest(
		t,
		srv,
		http.MethodPost,
		"/",
		bytes.NewReader([]byte{0x00, 0x01, 0x02, 0xff}),
		map[string]string{
			"Content-Type": "application/octet-stream",
		},
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	m := decode(t, body)
	want := "data:application/octet-stream;base64,AAEC_w=="
	if m["data"] != want {
		t.Errorf("data = %v, want %v", m["data"], want)
	}
}

func TestPOSTBinaryNoContentTypeDefaults(t *testing.T) {
	srv := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader([]byte{0x01, 0x02}))
	req.Header.Del("Content-Type")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	m := decode(t, body)
	if !strings.HasPrefix(asString(t, m["data"]), "data:application/octet-stream;base64,") {
		t.Errorf("data = %v, want default octet-stream prefix", m["data"])
	}
}

func TestBodyMethods(t *testing.T) {
	srv := newTestServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			resp, body := doRequest(
				t,
				srv,
				method,
				"/",
				strings.NewReader(`{"m":1}`),
				map[string]string{
					"Content-Type": "application/json",
				},
			)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			m := decode(t, body)
			if m["method"] != method {
				t.Errorf("method = %v", m["method"])
			}
			j := asMap(t, m["json"])
			if asFloat(t, j["m"]) != 1 {
				t.Errorf("json.m = %v", j["m"])
			}
		})
	}
}

func TestInvalidJSON(t *testing.T) {
	srv := newTestServer(t)
	resp, body := doRequest(
		t,
		srv,
		http.MethodPost,
		"/",
		strings.NewReader("not-json"),
		map[string]string{
			"Content-Type": "application/json",
		},
	)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	m := decode(t, body)
	if int(asFloat(t, m["code"])) != http.StatusBadRequest {
		t.Errorf("code = %v", m["code"])
	}
	if m["error"] != "Bad Request" {
		t.Errorf("error = %v", m["error"])
	}
	if _, ok := m["detail"].(string); !ok {
		t.Errorf("detail missing or wrong type: %v", m["detail"])
	}
}

func TestInvalidForm(t *testing.T) {
	srv := newTestServer(t)
	resp, _ := doRequest(
		t,
		srv,
		http.MethodPost,
		"/",
		strings.NewReader("bad=%ZZ"),
		map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		},
	)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestOversizedBody(t *testing.T) {
	srv := newTestServer(t)
	big := bytes.Repeat([]byte{'a'}, srvMaxBodyBytes+1)
	resp, body := doRequest(t, srv, http.MethodPost, "/", bytes.NewReader(big), map[string]string{
		"Content-Type": "application/octet-stream",
	})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf(
			"status = %d, want %d (body=%s)",
			resp.StatusCode,
			http.StatusRequestEntityTooLarge,
			body,
		)
	}
}

type unsizedReader struct{ r io.Reader }

func (u unsizedReader) Read(p []byte) (int, error) { return u.r.Read(p) }

func TestOversizedChunked(t *testing.T) {
	srv := newTestServer(t)
	big := bytes.Repeat([]byte{'a'}, srvMaxBodyBytes+1)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", unsizedReader{bytes.NewReader(big)})
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

func TestEmptyPOST(t *testing.T) {
	srv := newTestServer(t)
	resp, body := doRequest(t, srv, http.MethodPost, "/", nil, map[string]string{
		"Content-Type": "application/json",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	m := decode(t, body)
	if _, ok := m["json"]; ok {
		t.Errorf("empty body should not populate json")
	}
}

func TestWriteJSONMarshalError(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, make(chan int))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(logs.String(), "json encode error") {
		t.Errorf("expected log to mention json encode error, got: %s", logs.String())
	}
}

type errResponseWriter struct {
	header http.Header
	status int
}

func (e *errResponseWriter) Header() http.Header {
	if e.header == nil {
		e.header = http.Header{}
	}
	return e.header
}
func (e *errResponseWriter) WriteHeader(status int)      { e.status = status }
func (e *errResponseWriter) Write(_ []byte) (int, error) { return 0, io.ErrShortWrite }

func TestWriteJSONWriteError(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	ew := &errResponseWriter{}
	writeJSON(ew, http.StatusOK, map[string]any{"a": 1})

	if ew.status != http.StatusOK {
		t.Errorf(
			"status = %d, want %d (encode succeeded, only body write should fail)",
			ew.status,
			http.StatusOK,
		)
	}
	if !strings.Contains(logs.String(), "response write error") {
		t.Errorf("expected log to mention response write error, got: %s", logs.String())
	}
}

func TestURLOmitsRootPath(t *testing.T) {
	srv := newTestServer(t)
	resp, body := doRequest(t, srv, http.MethodGet, "/", nil, map[string]string{
		headerEchoHost:   "example.com",
		headerEchoScheme: "https",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	m := decode(t, body)
	if m["url"] != "https://example.com" {
		t.Errorf("url = %v, want bare host (no trailing slash)", m["url"])
	}
}

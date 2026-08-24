package dependencycapture

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

const redactedValue = "[REDACTED]"

var sensitiveHeaders = map[string]struct{}{
	"authorization": {}, "proxy-authorization": {}, "cookie": {}, "set-cookie": {},
}
var sensitiveQueryKeys = map[string]struct{}{
	"access_token": {}, "token": {}, "api_key": {}, "apikey": {}, "key": {},
	"secret": {}, "password": {}, "passwd": {},
}

type previewReader struct {
	reader  io.ReadCloser
	preview []byte
	limit   int
	total   int64
	capture bool
}

func newPreviewReader(reader io.ReadCloser, headers http.Header, limit int) *previewReader {
	return &previewReader{reader: reader, limit: limit, capture: bodyCaptureAllowed(headers)}
}
func (r *previewReader) Read(value []byte) (int, error) {
	n, err := r.reader.Read(value)
	r.total += int64(n)
	if r.capture && len(r.preview) < r.limit {
		remaining := r.limit - len(r.preview)
		if remaining > n {
			remaining = n
		}
		r.preview = append(r.preview, value[:remaining]...)
	}
	return n, err
}
func (r *previewReader) Close() error { return r.reader.Close() }

type previewWriter struct {
	preview []byte
	limit   int
	total   int64
	capture bool
}

func newPreviewWriter(headers http.Header, limit int) *previewWriter {
	return &previewWriter{limit: limit, capture: bodyCaptureAllowed(headers)}
}
func (w *previewWriter) observe(value []byte) {
	w.total += int64(len(value))
	if !w.capture || len(w.preview) >= w.limit {
		return
	}
	remaining := w.limit - len(w.preview)
	if remaining > len(value) {
		remaining = len(value)
	}
	w.preview = append(w.preview, value[:remaining]...)
}

func capturedBody(headers http.Header, preview []byte, size int64, limit int) tunnelprotocol.CapturedBody {
	contentType := normalizedContentType(headers.Get("Content-Type"))
	result := tunnelprotocol.CapturedBody{ContentType: contentType, SizeBytes: size}
	if encoding := strings.TrimSpace(headers.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		result.Reason = "encoded_content"
		return result
	}
	if !isTextualContentType(contentType) {
		result.Reason = "binary_content"
		return result
	}
	if len(preview) > limit {
		preview = preview[:limit]
	}
	if !utf8.Valid(preview) {
		valid := false
		if size > int64(len(preview)) {
			for removed := 1; removed <= 3 && removed <= len(preview); removed++ {
				candidate := preview[:len(preview)-removed]
				if utf8.Valid(candidate) {
					preview = candidate
					valid = true
					break
				}
			}
		}
		if !valid {
			result.Reason = "invalid_text"
			return result
		}
	}
	result.Captured = true
	result.Content = string(preview)
	result.CapturedBytes = int64(len(preview))
	result.Truncated = size > int64(len(preview))
	return result
}

func bodyCaptureAllowed(headers http.Header) bool {
	encoding := strings.TrimSpace(headers.Get("Content-Encoding"))
	return (encoding == "" || strings.EqualFold(encoding, "identity")) && isTextualContentType(normalizedContentType(headers.Get("Content-Type")))
}
func normalizedContentType(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		mediaType = strings.TrimSpace(strings.Split(value, ";")[0])
	}
	return strings.ToLower(mediaType)
}
func isTextualContentType(contentType string) bool {
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	if contentType == "application/json" || (strings.HasPrefix(contentType, "application/") && strings.HasSuffix(contentType, "+json")) {
		return true
	}
	if contentType == "application/xml" || contentType == "text/xml" || contentType == "application/soap+xml" || (strings.HasPrefix(contentType, "application/") && strings.HasSuffix(contentType, "+xml")) {
		return true
	}
	return contentType == "application/x-www-form-urlencoded"
}
func redactHeaders(source http.Header) map[string][]string {
	result := make(map[string][]string, len(source))
	for name, values := range source {
		copied := append([]string(nil), values...)
		if _, sensitive := sensitiveHeaders[strings.ToLower(name)]; sensitive {
			for index := range copied {
				copied[index] = redactedValue
			}
		}
		result[name] = copied
	}
	return result
}
func redactRawQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	for index, part := range parts {
		name, _, _ := strings.Cut(part, "=")
		decoded, err := url.QueryUnescape(name)
		if err == nil {
			if _, sensitive := sensitiveQueryKeys[strings.ToLower(decoded)]; sensitive {
				parts[index] = name + "=" + redactedValue
			}
		}
	}
	return strings.Join(parts, "&")
}

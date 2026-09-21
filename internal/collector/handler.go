package collector

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"

	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// contentTypeProtoBuf is the only content type accepted for OTLP
// protobuf uploads, per the OTLP/HTTP specification.
const contentTypeProtoBuf = "application/x-protobuf"

// maxBodyBytes bounds request size to protect the server from
// oversized payloads. 32 MiB comfortably fits typical trace batches.
const maxBodyBytes = 32 << 20

// Handler returns an http.Handler that accepts OTLP/HTTP trace exports
// at the standard /v1/traces endpoint.
//
// Behavior follows the OTLP/HTTP spec: only application/x-protobuf is
// accepted (415 otherwise), gzip and deflate encodings are transparently
// decoded, malformed protobufs yield 422, and a successful request is
// answered with an empty ExportTraceServiceResponse using the same
// content type as the request. Decoding happens synchronously so that
// clients learn about protocol errors immediately; downstream storage
// is asynchronous by design.
func Handler(decode func([]SpanWithProject)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != contentTypeProtoBuf {
			http.Error(w, "unsupported content type: "+ct, http.StatusUnsupportedMediaType)
			return
		}

		body, err := decodeBody(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
			return
		}

		req := &tracepb.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(body, req); err != nil {
			http.Error(w, "request body is not a valid ExportTraceServiceRequest", http.StatusUnprocessableEntity)
			return
		}

		spans := DecodeSpans(req)
		slog.Info("ingested spans", "count", len(spans), "remote", r.RemoteAddr)
		for _, p := range spans {
			s := p.Span
			slog.Info("span",
				"project", p.Project,
				"trace_id", s.TraceID,
				"span_id", s.SpanID,
				"parent_id", s.ParentID,
				"name", s.Name,
				"kind", s.SpanKind,
				"status", s.StatusCode,
				"start", s.StartTime,
				"end", s.EndTime,
			)
		}
		if decode != nil {
			decode(spans)
		}

		// Reply with an empty response message; the spec requires the
		// response Content-Type to match the request's.
		resp := &tracepb.ExportTraceServiceResponse{}
		respBytes, err := proto.Marshal(resp)
		if err != nil {
			http.Error(w, "failed to marshal response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentTypeProtoBuf)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBytes)
	})
}

// decodeBody reads the request body, honoring gzip and deflate
// content encodings. An unsupported encoding yields an error.
func decodeBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()

	var reader io.Reader = r.Body
	switch enc := r.Header.Get("Content-Encoding"); enc {
	case "":
		// no encoding
	case "gzip":
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	case "deflate":
		reader = flate.NewReader(reader)
	default:
		return nil, errUnsupportedEncoding{enc}
	}

	var buf bytes.Buffer
	// Cap what we are willing to buffer regardless of declared size.
	if _, err := io.Copy(&buf, io.LimitReader(reader, maxBodyBytes+1)); err != nil {
		return nil, err
	}
	if buf.Len() > maxBodyBytes {
		return nil, errBodyTooLarge{}
	}
	return buf.Bytes(), nil
}

type errUnsupportedEncoding struct{ enc string }

func (e errUnsupportedEncoding) Error() string {
	return "unsupported content encoding: " + e.enc
}

type errBodyTooLarge struct{}

func (errBodyTooLarge) Error() string { return "request body too large" }

package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

// Messages for a refused request body. Neither repeats the input: a body can
// hold a Data Key or target credentials, and the client already has it.
const (
	msgMalformedBody = "malformed request body"
	msgBodyTooLarge  = "request body too large"
	msgBodyNotJSON   = "request body must be declared as application/json"
)

// decodeJSONBody reads the request body into dst, which must be a pointer, and
// answers the request itself when the body is refused. It is the only way this
// package reads a request body (TestRequestBodiesGoThroughDecodeJSONBody).
//
// A body is read only when the request declares it as JSON (declaresJSON);
// otherwise it is answered 415 before a byte is read (#92). It is accepted
// when it is at most limit bytes and holds exactly one JSON value whose
// fields are all known to dst, at every depth. An empty body is refused, as
// is anything after the value but whitespace. A body over the limit is
// answered 413, anything else refused 400.
//
// Unknown fields are refused because a misspelt one would otherwise be
// dropped and the request carried out without it: a target saved without the
// credentials the admin typed, a schedule read as "disabled" (#90).
func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	if !declaresJSON(r.Header) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", msgBodyNotJSON)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		err = expectEOF(dec)
	}
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", msgBodyTooLarge)
		return false
	}
	writeError(w, http.StatusBadRequest, "bad_request", msgMalformedBody)
	return false
}

// declaresJSON reports whether the request declares its body as JSON: one
// Content-Type of application/json, in any case, without a charset or with
// charset utf-8 (JSON exchanged between systems is UTF-8, RFC 8259 §8.1).
// application/json defines no parameters, so any other is ignored.
//
// A browser sends text/plain and form bodies from any site without asking
// first; a JSON content type makes it ask (CORS preflight). Requiring one
// keeps that check in front of every body this API reads.
func declaresJSON(h http.Header) bool {
	values := h.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" {
		return false
	}
	charset, ok := params["charset"]
	return !ok || strings.EqualFold(charset, "utf-8")
}

// errTrailingData is what a body with something after its JSON value fails
// with.
var errTrailingData = errors.New("api: data after the JSON value")

// expectEOF reports whether dec has nothing left but whitespace. Reading on
// rather than asking dec.More keeps a limit exceeded by trailing bytes a 413.
func expectEOF(dec *json.Decoder) error {
	var extra json.RawMessage
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return err
	default:
		return errTrailingData
	}
}

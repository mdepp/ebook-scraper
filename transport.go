package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// For whatever reason, Cloudflare sometimes doesn't like the default http
// transport, but will accept requests from curl so long as the user agent
// string is changed. CurlTransport calls curl in a subprocess and is useful
// for those cases.
type CurlTransport struct {
}

const DELIMITER = "\n\n\n"

func (t CurlTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	args := []string{
		request.URL.String(), "--compressed", "--silent", "--write-out", fmt.Sprintf("%s%%{json}%s%%{header_json}", DELIMITER, DELIMITER), "-X", request.Method,
	}
	for key, values := range request.Header {
		for _, value := range values {
			args = append(args, "-H", fmt.Sprintf("%s: %s", key, value))
		}
	}
	out, err := exec.Command("/usr/bin/curl", args...).Output()
	if err != nil {
		return nil, err
	}
	chunks := rsplit(out, []byte(DELIMITER), 3)

	body := chunks[0]
	var extra struct {
		ResponseCode int    `json:"response_code"`
		HTTPVersion  string `json:"http_version"`
	}
	if err := json.Unmarshal(chunks[1], &extra); err != nil {
		return nil, err
	}

	var header http.Header
	if err := json.Unmarshal(chunks[2], &header); err != nil {
		return nil, err
	}
	// Canonicalize header keys
	for key, values := range header {
		header.Del(key)
		for _, value := range values {
			header.Add(key, value)
		}
	}

	major, minor, err := parseHTTPVersion(extra.HTTPVersion)
	if err != nil {
		return nil, err
	}

	response := &http.Response{
		Status:           fmt.Sprint(extra.ResponseCode),
		StatusCode:       extra.ResponseCode,
		Proto:            fmt.Sprintf("HTTP/%s", extra.HTTPVersion),
		ProtoMajor:       major,
		ProtoMinor:       minor,
		Header:           header,
		Body:             io.NopCloser(bytes.NewReader(body)),
		ContentLength:    int64(len(body)),
		TransferEncoding: []string{},
		Close:            true,
		Uncompressed:     false,
		Trailer:          http.Header{},
		Request:          request,
		TLS:              request.TLS,
	}
	return response, nil
}

func rsplit(s, sep []byte, n int) [][]byte {
	parts := bytes.Split(s, sep)
	if len(parts) < n {
		return parts
	}
	return append([][]byte{bytes.Join(parts[:len(parts)-n+1], sep)}, parts[len(parts)-n+1:]...)
}

func parseHTTPVersion(versionString string) (int, int, error) {
	major, minor, found := strings.Cut(versionString, ".")
	if !found {
		major = versionString
		minor = "0"
	}
	majorInt, err := strconv.Atoi(major)
	if err != nil {
		return 0, 0, err
	}
	minorInt, err := strconv.Atoi(minor)
	if err != nil {
		return 0, 0, err
	}
	return majorInt, minorInt, nil
}

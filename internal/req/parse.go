package req

import (
	"io"
	"net/http"

	"github.com/goccy/go-json"
)

func ParseAnthropic(r *http.Request) (*Request, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	req.RawBody = body
	return &req, nil
}

func ParsePassthrough(r *http.Request) (*Request, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return &Request{RawBody: body}, nil
}

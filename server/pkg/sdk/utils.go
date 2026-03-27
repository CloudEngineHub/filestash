package sdk

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	. "github.com/mickael-kerjean/filestash/server/common"
)

func (this Filestash) request(method string, url string, body io.Reader) (io.ReadCloser, http.Header, error) {
	req, err := http.NewRequest(
		method,
		fmt.Sprintf("%s://localhost:%d%s", func() string {
			if Config.Get("general.force_ssl").Bool() {
				return "https"
			}
			return "http"
		}(), Config.Get("general.port").Int(), WithBase(url)),
		body,
	)
	if err != nil {
		return nil, nil, err
	}
	req.Host = Config.Get("general.host").String()
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", this.Token))
	req.Header.Set("X-Requested-With", "XmlHttpRequest")
	t := HTTPClient.Transport.(*TransformedTransport).Orig.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 0
	resp, err := (&http.Client{
		Timeout:   0,
		Transport: NewTransformedTransport(t),
	}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		switch resp.StatusCode {
		case 401, 403:
			return nil, nil, ErrPermissionDenied
		case 404:
			return nil, nil, ErrNotFound
		default:
			return nil, nil, ErrNotValid
		}
	}
	return resp.Body, resp.Header, nil
}

func (this Filestash) unmarshalResults(resp io.ReadCloser, data interface{}) error {
	defer resp.Close()
	wrapper := struct {
		Status  string          `json:"status"`
		Results json.RawMessage `json:"results"`
	}{}
	decoder := json.NewDecoder(resp)
	if err := decoder.Decode(&wrapper); err != nil {
		return NewError(fmt.Sprintf("Failed to parse JSON response: %s", err.Error()), 500)
	}
	if wrapper.Status != "ok" {
		return NewError(fmt.Sprintf("API returned error status: %s", wrapper.Status), 500)
	}
	if err := json.Unmarshal(wrapper.Results, data); err != nil {
		return NewError(fmt.Sprintf("Failed to unmarshal results: %s", err.Error()), 500)
	}
	return nil
}

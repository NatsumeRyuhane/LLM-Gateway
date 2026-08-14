package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxOutboundHeaderValueBytes = 8 << 10

// BearerCredential is provider-owned authorization material. Formatting always
// redacts it; only NewRequest can place its value on a newly constructed request.
type BearerCredential struct {
	value string
}

// NewBearerCredential validates already decrypted, route-bound provider
// material. Decryption and endpoint registration remain outside this slice.
func NewBearerCredential(value string) (BearerCredential, error) {
	if !validRequiredHeaderValue(value) || strings.ContainsAny(value, " \t") {
		return BearerCredential{}, errors.New("invalid provider bearer credential")
	}
	return BearerCredential{value: value}, nil
}

// String implements fmt.Stringer without revealing provider material.
func (BearerCredential) String() string { return "[REDACTED]" }

// GoString implements fmt.GoStringer without revealing provider material.
func (BearerCredential) GoString() string { return "provider.BearerCredential{[REDACTED]}" }

// RequestHeaders is the complete explicit outbound header allowlist. It has no
// generic map and cannot carry inbound identity, forwarding, cookie, proxy, or
// provider-authentication headers.
type RequestHeaders struct {
	ContentType string
	Accept      string
	UserAgent   string
}

// NewRequest constructs a fresh provider request from canonical values. It does
// not accept an inbound HTTP request or header map and never copies either.
func NewRequest(
	ctx context.Context,
	method string,
	endpoint string,
	body io.Reader,
	headers RequestHeaders,
	credential BearerCredential,
) (*http.Request, error) {
	if credential.value == "" {
		return nil, errors.New("provider bearer credential is required")
	}
	if !validOptionalHeaderValue(headers.ContentType) || !validOptionalHeaderValue(headers.Accept) || !validOptionalHeaderValue(headers.UserAgent) {
		return nil, errors.New("invalid provider request header value")
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, errors.New("construct provider request: invalid method or endpoint")
	}
	request.Header = make(http.Header, 4)
	request.Header.Set("Authorization", "Bearer "+credential.value)
	setAllowedHeader(request.Header, "Content-Type", headers.ContentType)
	setAllowedHeader(request.Header, "Accept", headers.Accept)
	setAllowedHeader(request.Header, "User-Agent", headers.UserAgent)
	return request, nil
}

func setAllowedHeader(header http.Header, name, value string) {
	if value != "" {
		header.Set(name, value)
	}
}

func validRequiredHeaderValue(value string) bool {
	return value != "" && validOptionalHeaderValue(value)
}

func validOptionalHeaderValue(value string) bool {
	if len(value) > maxOutboundHeaderValueBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

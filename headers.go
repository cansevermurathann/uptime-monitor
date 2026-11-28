package main

import (
	"errors"
	"net/http"
	"strings"
)

type headerExpectation struct{ name, value string }

// Validate before any network request; never include supplied values in errors.
func parseHeaderExpectations(raw []string) ([]headerExpectation, error) {
	var expectations []headerExpectation
	for _, entry := range raw {
		name, value, found := strings.Cut(entry, ":")
		if !found || !validHeaderName(name) || strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("expect-header requires a valid HTTP field name followed by :VALUE without line breaks")
		}
		value = strings.Trim(value, " \t")
		for i := 0; i < len(value); i++ {
			if (value[i] < 32 && value[i] != '\t') || value[i] == 127 {
				return nil, errors.New("expect-header value contains an invalid control character")
			}
		}
		expectations = append(expectations, headerExpectation{http.CanonicalHeaderKey(name), value})
	}
	return expectations, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return true
}

// Match each expectation against a complete field value. Do not split on commas:
// commas are meaningful inside some values, such as dates and quoted strings.
func headersMatch(headers http.Header, expectations []headerExpectation) bool {
	for _, expected := range expectations {
		found := false
		for _, actual := range headers.Values(expected.name) {
			if strings.Trim(actual, " \t") == expected.value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

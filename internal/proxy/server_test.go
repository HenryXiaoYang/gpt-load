package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"gpt-load/internal/failover"
	"gpt-load/internal/models"
)

func TestShouldFailoverOnResponseStatusSpecificBodyPhrase(t *testing.T) {
	statusMatcher, err := failover.ParseStatusCodeMatcher("400")
	if err != nil {
		t.Fatalf("ParseStatusCodeMatcher returned error: %v", err)
	}
	bodyMatcher, err := failover.ParseBodyPhraseMatcher("400:insufficient quota\n400:credit balance")
	if err != nil {
		t.Fatalf("ParseBodyPhraseMatcher returned error: %v", err)
	}
	group := &models.Group{
		FailoverStatusCodeMatcher: statusMatcher,
		FailoverBodyPhraseMatcher: bodyMatcher,
	}

	t.Run("matching phrase retries", func(t *testing.T) {
		resp := newTestResponse(http.StatusBadRequest, `{"error":"credit balance is too low"}`)

		shouldRetry, body, readErr, matchedPhrase := shouldFailoverOnResponse(resp, group)
		if readErr != nil {
			t.Fatalf("shouldFailoverOnResponse read error: %v", readErr)
		}
		if !shouldRetry {
			t.Fatal("expected response to trigger failover")
		}
		if matchedPhrase != "credit balance" {
			t.Fatalf("unexpected matched phrase: %q", matchedPhrase)
		}
		if !strings.Contains(string(body), "credit balance") {
			t.Fatalf("expected captured body to contain matched phrase, got %q", string(body))
		}
	})

	t.Run("unmatched phrase passes through", func(t *testing.T) {
		resp := newTestResponse(http.StatusBadRequest, `{"error":"invalid request payload"}`)

		shouldRetry, body, readErr, matchedPhrase := shouldFailoverOnResponse(resp, group)
		if readErr != nil {
			t.Fatalf("shouldFailoverOnResponse read error: %v", readErr)
		}
		if shouldRetry {
			t.Fatal("did not expect response to trigger failover")
		}
		if body != nil {
			t.Fatalf("expected no captured body, got %q", string(body))
		}
		if matchedPhrase != "" {
			t.Fatalf("unexpected matched phrase: %q", matchedPhrase)
		}

		passThroughBody, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read restored response body: %v", err)
		}
		if string(passThroughBody) != `{"error":"invalid request payload"}` {
			t.Fatalf("response body was not restored, got %q", string(passThroughBody))
		}
	})
}

func TestShouldFailoverOnResponseWildcardDoesNotOverrideStatusMatcher(t *testing.T) {
	bodyMatcher, err := failover.ParseBodyPhraseMatcher("*:temporary outage")
	if err != nil {
		t.Fatalf("ParseBodyPhraseMatcher returned error: %v", err)
	}
	group := &models.Group{
		FailoverBodyPhraseMatcher: bodyMatcher,
	}

	resp := newTestResponse(http.StatusBadRequest, `{"error":"invalid request payload"}`)
	shouldRetry, _, readErr, matchedPhrase := shouldFailoverOnResponse(resp, group)
	if readErr != nil {
		t.Fatalf("shouldFailoverOnResponse read error: %v", readErr)
	}
	if shouldRetry {
		t.Fatal("did not expect wildcard body rule to make unmatched status fail")
	}
	if matchedPhrase != "" {
		t.Fatalf("unexpected matched phrase: %q", matchedPhrase)
	}

	passThroughBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read restored response body: %v", err)
	}
	if string(passThroughBody) != `{"error":"invalid request payload"}` {
		t.Fatalf("response body was not restored, got %q", string(passThroughBody))
	}
}

func TestShouldFailoverOnResponseWildcardSkipsNonFailoverStatus(t *testing.T) {
	bodyMatcher, err := failover.ParseBodyPhraseMatcher("*:temporary outage")
	if err != nil {
		t.Fatalf("ParseBodyPhraseMatcher returned error: %v", err)
	}
	group := &models.Group{
		FailoverBodyPhraseMatcher: bodyMatcher,
	}

	resp := newTestResponse(http.StatusOK, `temporary outage`)
	shouldRetry, _, readErr, matchedPhrase := shouldFailoverOnResponse(resp, group)
	if readErr != nil {
		t.Fatalf("shouldFailoverOnResponse read error: %v", readErr)
	}
	if shouldRetry {
		t.Fatal("did not expect wildcard body rule to scan a non-failover status")
	}
	if matchedPhrase != "" {
		t.Fatalf("unexpected matched phrase: %q", matchedPhrase)
	}

	passThroughBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if string(passThroughBody) != "temporary outage" {
		t.Fatalf("response body should not have been consumed, got %q", string(passThroughBody))
	}
}

func newTestResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

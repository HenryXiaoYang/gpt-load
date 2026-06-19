package failover

import "testing"

func TestBodyPhraseMatcherMultiplePhrasesForSameStatus(t *testing.T) {
	matcher, err := ParseBodyPhraseMatcher("400:insufficient quota\n400:credit balance is too low")
	if err != nil {
		t.Fatalf("ParseBodyPhraseMatcher returned error: %v", err)
	}

	if !matcher.MightMatchStatus(400) {
		t.Fatal("expected matcher to apply to status 400")
	}
	if !matcher.HasStatusSpecificRule(400) {
		t.Fatal("expected status-specific rule for status 400")
	}

	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "first phrase",
			body: []byte(`{"error":{"message":"insufficient quota for this request"}}`),
		},
		{
			name: "second phrase",
			body: []byte(`{"error":{"message":"credit balance is too low"}}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, matched := matcher.Match(400, tt.body); !matched {
				t.Fatalf("expected body to match")
			}
		})
	}

	if _, matched := matcher.Match(400, []byte(`{"error":"bad request"}`)); matched {
		t.Fatal("did not expect unmatched body to match")
	}
}

func TestBodyPhraseMatcherWildcardStatus(t *testing.T) {
	matcher, err := ParseBodyPhraseMatcher("*:temporarily unavailable")
	if err != nil {
		t.Fatalf("ParseBodyPhraseMatcher returned error: %v", err)
	}

	if !matcher.MightMatchStatus(429) {
		t.Fatal("expected wildcard rule to apply to status 429")
	}
	if matcher.HasStatusSpecificRule(429) {
		t.Fatal("did not expect wildcard rule to be status-specific")
	}
	if _, matched := matcher.Match(429, []byte("service temporarily unavailable")); !matched {
		t.Fatal("expected wildcard phrase to match")
	}
}

func TestBodyPhraseMatcherRejectsInvalidRule(t *testing.T) {
	if _, err := ParseBodyPhraseMatcher("400"); err == nil {
		t.Fatal("expected missing phrase separator to fail")
	}
}

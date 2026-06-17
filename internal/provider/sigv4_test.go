package provider

import (
	"net/http"
	"testing"
	"time"
)

// TestSignV4_GetVanilla checks the signer against AWS's canonical SigV4
// "get-vanilla" known-answer test vector. The expected signature is AWS's
// published value (independently reproduced with openssl), so this catches any
// drift in the canonical request, string-to-sign, or key-derivation steps.
func TestSignV4_GetVanilla(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	creds := awsCreds{
		accessKey: "AKIDEXAMPLE",
		secretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}
	when := time.Date(2015, time.August, 30, 12, 36, 0, 0, time.UTC)

	signV4(req, nil, creds, "us-east-1", "service", when)

	if got := req.Header.Get("X-Amz-Date"); got != "20150830T123600Z" {
		t.Errorf("X-Amz-Date = %q", got)
	}
	const want = "AWS4-HMAC-SHA256 " +
		"Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, " +
		"Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestAWSURIEncodeSegment(t *testing.T) {
	// Unreserved chars pass through; everything else (notably ':' in Bedrock model
	// ids) is percent-encoded uppercase.
	cases := map[string]string{
		"anthropic.claude-3-5-sonnet-20240620-v1:0": "anthropic.claude-3-5-sonnet-20240620-v1%3A0",
		"a_b.c-d~e": "a_b.c-d~e",
		"x/y":       "x%2Fy",
		"a b":       "a%20b",
	}
	for in, want := range cases {
		if got := awsURIEncodeSegment(in); got != want {
			t.Errorf("awsURIEncodeSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

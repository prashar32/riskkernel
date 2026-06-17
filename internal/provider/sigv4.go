package provider

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 signing, hand-rolled so the Bedrock provider needs no
// AWS SDK dependency. Scope is exactly what Bedrock's runtime needs: a signed POST
// (or GET) with host + x-amz-date, plus content-type and x-amz-security-token when
// present. Verified against the AWS SigV4 "get-vanilla" known-answer test vector
// (see sigv4_test.go).

// awsCreds are the AWS credentials used to sign a request. sessionToken is set only
// for temporary (STS) credentials.
type awsCreds struct {
	accessKey    string
	secretKey    string
	sessionToken string
}

// signV4 signs req in place with AWS SigV4, setting the X-Amz-Date,
// X-Amz-Security-Token (when temporary creds), and Authorization headers. body is
// the exact request body that will be sent (nil/empty for a GET). The caller must
// have set req.URL such that EscapedPath() equals the path that will be sent on the
// wire (so the signed canonical URI matches the request) — see bedrock.go.
func signV4(req *http.Request, body []byte, c awsCreds, region, service string, t time.Time) {
	t = t.UTC()
	amzDate := t.Format("20060102T150405Z")
	dateStamp := t.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	if c.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.sessionToken)
	}

	// The headers we sign: host and x-amz-date always; content-type and the
	// session token when present. AWS requires lowercase names, sorted.
	headers := map[string]string{
		"host":       req.URL.Host,
		"x-amz-date": amzDate,
	}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		headers["content-type"] = ct
	}
	if c.sessionToken != "" {
		headers["x-amz-security-token"] = c.sessionToken
	}
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)

	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n)
		canonHeaders.WriteByte(':')
		canonHeaders.WriteString(strings.TrimSpace(headers[n]))
		canonHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		req.URL.RawQuery, // empty for our calls; assumed already canonical otherwise
		canonHeaders.String(),
		signedHeaders,
		hexSHA256(body),
	}, "\n")

	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+c.secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 "+
		"Credential="+c.accessKey+"/"+scope+", "+
		"SignedHeaders="+signedHeaders+", "+
		"Signature="+signature)
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// awsURIEncodeSegment percent-encodes a single path segment per AWS's UriEncode
// rules: every byte except the unreserved set (A-Z a-z 0-9 - _ . ~) is encoded.
// This is stricter than Go's url.PathEscape (which leaves ':' alone), and Bedrock
// model ids contain ':' (e.g. ...-v1:0) — so the signed path matches the wire path.
func awsURIEncodeSegment(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if strings.IndexByte(unreserved, ch) >= 0 {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

package s3

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// The transport is proved against real MinIO in the docker suite. What is here
// is everything that decides whether a request is made at all: the endpoint
// grammar, the key arithmetic, and the refusals. Those are the parts an
// operator meets, and the parts a container cannot exercise cheaply -- there is
// no MinIO configuration that produces "you gave me half a credential".

func TestResolveEndpointDefaultsToTLS(t *testing.T) {
	for name, tc := range map[string]struct {
		in     string
		host   string
		secure bool
		bad    bool
	}{
		"empty means AWS":       {in: "", host: DefaultEndpoint, secure: true},
		"a bare host means TLS": {in: "minio.example:9000", host: "minio.example:9000", secure: true},
		"https is explicit":     {in: "https://minio.example", host: "minio.example", secure: true},
		// The one way to ask for plaintext, and it has to be written
		// out. Nothing reaches it by default and no flag turns TLS off.
		"http is the only plaintext route": {in: "http://127.0.0.1:9000", host: "127.0.0.1:9000", secure: false},
		"another scheme is refused":        {in: "ftp://minio.example", bad: true},
		"a scheme with no host is refused": {in: "https://", bad: true},
	} {
		t.Run(name, func(t *testing.T) {
			host, secure, err := resolveEndpoint(tc.in)
			if tc.bad {
				if err == nil {
					t.Fatalf("resolveEndpoint(%q) was accepted", tc.in)
				}
				if domain.AsError(err).Hint == "" {
					t.Error("a refusal about an endpoint must say what a good one looks like")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if host != tc.host {
				t.Errorf("host = %q, want %q", host, tc.host)
			}
			if secure != tc.secure {
				t.Errorf("secure = %v, want %v -- a target reached without TLS "+
					"is a target somebody else can be", secure, tc.secure)
			}
		})
	}
}

// TestHalfACredentialIsRefused. No credentials at all is legitimate -- an
// instance role, a public MinIO in a test -- so it passes through. One of the
// two is always a mistake, and the moment it fails is otherwise a signature
// error from a bucket, which reads as a permissions problem.
func TestHalfACredentialIsRefused(t *testing.T) {
	for name, creds := range map[string]ports.TargetCredentials{
		"an id with no secret": {AccessKeyID: "AKIAEXAMPLE"},
		"a secret with no id":  {SecretAccessKey: "s3kr3t"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New().client(ports.TargetRef{Scheme: "s3", Credentials: creds})
			if err == nil {
				t.Fatal("half a credential was accepted")
			}
			if !strings.Contains(domain.AsError(err).Hint, "instance") {
				t.Errorf("the hint must say what to do when you meant to supply "+
					"neither: %q", domain.AsError(err).Hint)
			}
		})
	}
}

// TestNeitherHalfOfACredentialIsFine, because an instance role is a real
// configuration and refusing it would make this adapter unusable on EC2.
func TestNeitherHalfOfACredentialIsFine(t *testing.T) {
	if _, err := New().client(ports.TargetRef{Scheme: "s3"}); err != nil {
		t.Fatalf("an unauthenticated client was refused: %v", err)
	}
}

func TestClientsAreReusedPerEndpoint(t *testing.T) {
	target := New()
	ref := ports.TargetRef{Scheme: "s3", Credentials: ports.TargetCredentials{
		AccessKeyID: "AKIA", SecretAccessKey: "s3kr3t", Endpoint: "http://minio.example:9000",
	}}

	first, err := target.client(ref)
	if err != nil {
		t.Fatal(err)
	}
	second, err := target.client(ref)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("a second client was built for the same endpoint; one backup would " +
			"open a connection pool per component")
	}
}

// TestABucketlessURLIsRefused before any request is made, because
// "NoSuchBucket" arriving mid-push reads as a transfer failure when it is a
// configuration mistake.
func TestABucketlessURLIsRefused(t *testing.T) {
	_, err := New().store(context.Background(), ports.TargetRef{Scheme: "s3", Path: "/"})
	if err == nil {
		t.Fatal("a target naming no bucket was accepted")
	}
}

// TestAComponentPathCannotEscapeThePrefix.
//
// Object keys are not paths and `..` is a legal character sequence in one --
// which is exactly why it has to be refused rather than assumed harmless. A
// manifest on a target is a file this manager may not have written, and its
// component paths decide what a fetch reads and what a removal deletes.
func TestAComponentPathCannotEscapeThePrefix(t *testing.T) {
	store := &bucketStore{prefix: "demo/backups"}

	for name, key := range map[string]string{
		"a parent reference":   "../../secrets.sops.yaml",
		"one buried in a path": "20260101T000000Z/../../../etc/passwd",
		"an absolute key":      "/etc/passwd",
		"an empty key":         "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.resolve(key); err == nil {
				t.Fatalf("resolve(%q) was accepted, so a hostile manifest decides "+
					"what this machine reads and deletes", key)
			}
		})
	}

	got, err := store.resolve("20260101T000000Z/database.sql.age")
	if err != nil {
		t.Fatal(err)
	}
	if want := "demo/backups/20260101T000000Z/database.sql.age"; got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
}

func TestJoinKeyIgnoresEmptyAndDuplicateSeparators(t *testing.T) {
	for name, tc := range map[string]struct {
		parts []string
		want  string
	}{
		"a bare prefix":       {parts: []string{"demo", "backup.json"}, want: "demo/backup.json"},
		"no prefix at all":    {parts: []string{"", "backup.json"}, want: "backup.json"},
		"stray separators":    {parts: []string{"/demo/", "/backups/"}, want: "demo/backups"},
		"nothing to join":     {parts: []string{"", ""}, want: ""},
		"three levels of key": {parts: []string{"demo", "id", "db.age"}, want: "demo/id/db.age"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := joinKey(tc.parts...); got != tc.want {
				t.Errorf("joinKey(%q) = %q, want %q", tc.parts, got, tc.want)
			}
		})
	}
}

// TestRefusedCredentialsAreNamedWithTheirRemedy. This failure happens most
// often on a rebuilt machine, where the stored keys are the ones that died with
// the old host -- so the remedy has to name the escape hatch.
func TestRefusedCredentialsAreNamedWithTheirRemedy(t *testing.T) {
	ref := ports.TargetRef{Scheme: "s3", Path: "/bucket", URL: "s3://bucket"}

	for _, code := range []string{"InvalidAccessKeyId", "SignatureDoesNotMatch", "AccessDenied"} {
		err := New().unreachable(ref, minio.ErrorResponse{Code: code})
		if !strings.Contains(domain.AsError(err).Hint, "--credentials-file") {
			t.Errorf("%s: the hint does not name the escape hatch: %q",
				code, domain.AsError(err).Hint)
		}
	}

	// Anything else is a reachability problem and must not be reported as a
	// credential one: they send an operator to different places.
	err := New().unreachable(ref, errors.New("dial tcp: connection refused"))
	if strings.Contains(domain.AsError(err).Hint, "--credentials-file") {
		t.Error("a network failure was reported as a credentials failure")
	}
}

func TestNotFoundRecognisesBothShapesTheAPIUses(t *testing.T) {
	if !notFound(minio.ErrorResponse{Code: "NoSuchKey"}) {
		t.Error("NoSuchKey was not recognised, so a missing backup would be reported " +
			"as an unreachable target")
	}
	if !notFound(minio.ErrorResponse{StatusCode: http.StatusNotFound}) {
		t.Error("a bare 404 was not recognised; not every S3-compatible store sends a code")
	}
	if notFound(errors.New("connection refused")) {
		t.Error("a network failure was mistaken for a missing backup")
	}
}

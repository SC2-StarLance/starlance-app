package main

import (
	"strings"
	"testing"

	"github.com/jedisct1/go-minisign"
)

// A throwaway keypair, used only by this test. It has no release authority.
const (
	testMinisignPublicKey = `untrusted comment: minisign public key 391E5485A582D2E1
RWTh0oKlhVQeOWzzW1tgmk+cVvwo6Fiab7HVbc3FUJTphPwwuLAhHohD
`
	testMinisignSecretKey = `untrusted comment: test key, never use online
RWQAAEIyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA4dKCpYVUHjlxQXDPGdf8nztArKHTQC3Hvtr2iOeOQldkJdG9ZAuMN2zzW1tgmk+cVvwo6Fiab7HVbc3FUJTphPwwuLAhHohDAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
`
)

func testSignature(t *testing.T, data []byte) []byte {
	t.Helper()
	sk, err := minisign.DecodePrivateKey(testMinisignSecretKey)
	if err != nil {
		t.Fatalf("decode test key: %v", err)
	}
	sig, err := sk.Sign(data, minisign.SignOptions{Hashed: true})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig.Encode()
}

func TestMinisignValidatorAcceptsSignedAsset(t *testing.T) {
	release := []byte("zip payload")
	v := minisignValidator{publicKey: testMinisignPublicKey}
	if name := v.GetValidationAssetName("sc2-replay-uploader-linux-amd64.zip"); name != "sc2-replay-uploader-linux-amd64.zip.minisig" {
		t.Fatalf("validation asset name = %q", name)
	}
	if err := v.Validate("sc2-replay-uploader-linux-amd64.zip", release, testSignature(t, release)); err != nil {
		t.Fatalf("signed asset rejected: %v", err)
	}
}

func TestMinisignValidatorRejectsBadInputs(t *testing.T) {
	v := minisignValidator{publicKey: testMinisignPublicKey}
	sig := testSignature(t, []byte("zip payload"))
	if err := v.Validate("a.zip", []byte("tampered"), sig); err == nil {
		t.Error("tampered asset must be rejected")
	}
	if err := v.Validate("a.zip", []byte("zip payload"), nil); err == nil {
		t.Error("unsigned release must be rejected")
	}
	if err := v.Validate("a.zip", []byte("zip payload"), []byte("not a signature")); err == nil {
		t.Error("garbage signature must be rejected")
	}
	other := minisignValidator{publicKey: strings.Replace(testMinisignPublicKey, "RWTh0oKl", "RWTA0oKl", 1)}
	if err := other.Validate("a.zip", []byte("zip payload"), sig); err == nil {
		t.Error("a different public key must reject the signature")
	}
	if err := v.Validate("a.zip", []byte("zip payload"), sig); err != nil {
		t.Fatalf("control case failed: %v", err)
	}
}

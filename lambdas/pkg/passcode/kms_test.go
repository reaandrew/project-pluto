package passcode

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// fakeKMS is a minimal in-memory KMS that round-trips Encrypt/Decrypt.
// It does NOT actually encrypt — the "ciphertext" is just `keyID|cleartext`
// — but it's enough to verify the call shape and the base64 round-trip.
type fakeKMS struct {
	encryptErr error
	decryptErr error
	gotEncrypt *kms.EncryptInput
	gotDecrypt *kms.DecryptInput
}

func (f *fakeKMS) Encrypt(_ context.Context, in *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	f.gotEncrypt = in
	if f.encryptErr != nil {
		return nil, f.encryptErr
	}
	blob := []byte(aws.ToString(in.KeyId) + "|" + string(in.Plaintext))
	return &kms.EncryptOutput{CiphertextBlob: blob}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	f.gotDecrypt = in
	if f.decryptErr != nil {
		return nil, f.decryptErr
	}
	// reverse the fake encrypt: split on "|"
	parts := strings.SplitN(string(in.CiphertextBlob), "|", 2)
	if len(parts) != 2 {
		return nil, errors.New("fakeKMS: malformed ciphertext")
	}
	return &kms.DecryptOutput{Plaintext: []byte(parts[1])}, nil
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	fake := &fakeKMS{}
	const key = "arn:aws:kms:eu-west-2:123:key/uuid"
	const cleartext = "ABCDEFGH"

	cipher, err := EncryptCleartext(context.Background(), fake, key, cleartext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if cipher == "" {
		t.Fatal("ciphertext is empty")
	}
	// the ciphertext must be base64 — decode without error
	if _, err := base64.StdEncoding.DecodeString(cipher); err != nil {
		t.Errorf("ciphertext is not base64: %v", err)
	}
	if aws.ToString(fake.gotEncrypt.KeyId) != key {
		t.Errorf("KeyId not threaded into Encrypt input")
	}

	plain, err := DecryptCleartext(context.Background(), fake, cipher)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if plain != cleartext {
		t.Errorf("round-trip drift: got %q, want %q", plain, cleartext)
	}
}

func TestEncryptCleartextRejectsEmptyArgs(t *testing.T) {
	cases := map[string]struct {
		client    KMSAPI
		keyID     string
		cleartext string
	}{
		"nil client":      {nil, "key", "ABCD"},
		"empty key":       {&fakeKMS{}, "", "ABCD"},
		"empty cleartext": {&fakeKMS{}, "key", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := EncryptCleartext(context.Background(), tc.client, tc.keyID, tc.cleartext)
			if err == nil {
				t.Errorf("expected error for %q", name)
			}
		})
	}
}

func TestDecryptCleartextRejectsEmptyArgs(t *testing.T) {
	cases := map[string]struct {
		client KMSAPI
		cipher string
	}{
		"nil client":   {nil, base64.StdEncoding.EncodeToString([]byte("anything"))},
		"empty cipher": {&fakeKMS{}, ""},
		"non-base64":   {&fakeKMS{}, "###not-base64###"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecryptCleartext(context.Background(), tc.client, tc.cipher)
			if err == nil {
				t.Errorf("expected error for %q", name)
			}
		})
	}
}

func TestEncryptSurfacesSDKError(t *testing.T) {
	fake := &fakeKMS{encryptErr: errors.New("KMS down")}
	_, err := EncryptCleartext(context.Background(), fake, "key", "ABCD")
	if err == nil || !strings.Contains(err.Error(), "KMS Encrypt") {
		t.Errorf("expected KMS Encrypt error, got %v", err)
	}
}

func TestDecryptSurfacesSDKError(t *testing.T) {
	fake := &fakeKMS{decryptErr: errors.New("denied")}
	cipher := base64.StdEncoding.EncodeToString([]byte("anything"))
	_, err := DecryptCleartext(context.Background(), fake, cipher)
	if err == nil || !strings.Contains(err.Error(), "KMS Decrypt") {
		t.Errorf("expected KMS Decrypt error, got %v", err)
	}
}

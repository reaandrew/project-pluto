package passcode

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// KMSAPI is the subset of the KMS SDK we depend on. Defined as an
// interface so tests can supply a fake.
type KMSAPI interface {
	Encrypt(ctx context.Context, in *kms.EncryptInput, opts ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, in *kms.DecryptInput, opts ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// EncryptCleartext encrypts the cleartext passcode under the project's
// passcode-cleartext-${env} CMK and returns the base64-encoded ciphertext
// blob. The result is what the publisher Lambda writes into
// `Website.passcodeCipher` (a DynamoDB string attribute).
//
// keyID is the CMK ARN (or alias). cleartext must NOT be logged or
// emitted in events — see package docstring.
func EncryptCleartext(ctx context.Context, client KMSAPI, keyID, cleartext string) (string, error) {
	if client == nil {
		return "", errors.New("passcode: nil KMS client")
	}
	if keyID == "" {
		return "", errors.New("passcode: KMS keyID required")
	}
	if cleartext == "" {
		return "", errors.New("passcode: cleartext is empty")
	}
	out, err := client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String(keyID),
		Plaintext: []byte(cleartext),
	})
	if err != nil {
		return "", fmt.Errorf("passcode: KMS Encrypt: %w", err)
	}
	return base64.StdEncoding.EncodeToString(out.CiphertextBlob), nil
}

// DecryptCleartext is the inverse — used by the email-draft Lambda when
// it needs to substitute the passcode into the email body for a single
// outbound send. The plaintext is held in process memory only for the
// duration of that Bedrock + SES call chain; after the email is sent,
// the publisher's wipe job clears `Website.passcodeCipher` so this path
// can no longer succeed.
func DecryptCleartext(ctx context.Context, client KMSAPI, ciphertextB64 string) (string, error) {
	if client == nil {
		return "", errors.New("passcode: nil KMS client")
	}
	if ciphertextB64 == "" {
		return "", errors.New("passcode: ciphertext is empty")
	}
	blob, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", fmt.Errorf("passcode: base64 decoding ciphertext: %w", err)
	}
	out, err := client.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: blob,
	})
	if err != nil {
		return "", fmt.Errorf("passcode: KMS Decrypt: %w", err)
	}
	return string(out.Plaintext), nil
}

// tools/signrelease signs release files with the project's minisign key so
// installed copies can verify an update before replacing themselves.
//
// The secret key is read from MINISIGN_SECRET_KEY (the full minisign secret key
// file text) and must stay unencrypted, so CI can use it without a passphrase.
// Each input file gets a matching <file>.minisig next to it.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/jedisct1/go-minisign"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "signrelease:", err)
		os.Exit(1)
	}
}

func run(files []string) error {
	if len(files) == 0 {
		return fmt.Errorf("usage: signrelease <file>...")
	}
	key := strings.TrimSpace(os.Getenv("MINISIGN_SECRET_KEY"))
	if key == "" {
		return fmt.Errorf("MINISIGN_SECRET_KEY is not set")
	}
	key = strings.ReplaceAll(key, "\r\n", "\n")
	if !strings.Contains(key, "\n") {
		key = "untrusted comment: minisign secret key\n" + key
	}
	sk, err := minisign.DecodePrivateKey(key)
	if err != nil {
		return fmt.Errorf("read secret key: %w", err)
	}
	defer sk.Wipe()
	if sk.IsEncrypted() {
		return fmt.Errorf("secret key is passphrase-protected; CI needs an unencrypted key")
	}

	pub, err := minisign.NewPublicKeyFromFile("minisign.pub")
	if err != nil {
		return fmt.Errorf("read minisign.pub: %w", err)
	}

	for _, name := range files {
		sig, err := sk.SignFile(name, minisign.SignOptions{Hashed: true})
		if err != nil {
			return fmt.Errorf("%s: sign: %w", name, err)
		}
		ok, err := pub.VerifyFromFile(name, sig)
		if err != nil || !ok {
			return fmt.Errorf("%s: signature does not verify against minisign.pub (wrong key?)", name)
		}
		out := name + ".minisig"
		if err := os.WriteFile(out, sig.Encode(), 0o644); err != nil {
			return err
		}
		fmt.Println("signed", out)
	}
	return nil
}

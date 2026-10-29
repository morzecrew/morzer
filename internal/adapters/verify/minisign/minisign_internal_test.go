package minisign

import (
	"testing"

	gominisign "github.com/jedisct1/go-minisign"
)

// The key id a statement names is the one `minisign` prints for the key, so an
// auditor can match it by eye. This is the public key from minisign's own
// documentation, printed there as `minisign public key E7620F1842B4E81F`.
func TestAKeyIDIsTheOneMinisignPrints(t *testing.T) {
	pk, err := gominisign.NewPublicKey("RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3")
	if err != nil {
		t.Fatal(err)
	}
	if got := keyID(pk); got != "E7620F1842B4E81F" {
		t.Errorf("key id %s, want E7620F1842B4E81F", got)
	}
}

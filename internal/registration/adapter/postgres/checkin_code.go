package postgres

import (
	"fmt"
	"io"
)

// Crockford's base32 alphabet gives 32^8 codes without ambiguous I, L, O or U.
// Input validation deliberately remains broader so existing codes still work.
const checkinCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func generateCheckinCode(random io.Reader) (string, error) {
	var code [8]byte
	if _, err := io.ReadFull(random, code[:]); err != nil {
		return "", fmt.Errorf("generate check-in code: %w", err)
	}
	for i, b := range code {
		// 32 divides 256: each symbol has exactly eight byte preimages.
		code[i] = checkinCodeAlphabet[b&31]
	}
	return string(code[:]), nil
}

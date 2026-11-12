package postgres

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestGenerateCheckinCodeUniformAlphabet(t *testing.T) {
	// All 256 byte values must map evenly onto exactly these 32 symbols.
	// Hex generation, modulo bias, and accidental alphabet changes break this.
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	counts := make(map[byte]int)
	for i := 0; i < 256; i++ {
		code, err := generateCheckinCode(bytes.NewReader(bytes.Repeat([]byte{byte(i)}, 8)))
		if err != nil || len(code) != 8 || code != strings.Repeat(string(alphabet[i%32]), 8) {
			t.Fatalf("input %d: code=%q err=%v", i, code, err)
		}
		counts[code[0]]++
	}
	for _, symbol := range []byte(alphabet) {
		if counts[symbol] != 8 {
			t.Fatalf("symbol %c: %d preimages, want 8", symbol, counts[symbol])
		}
	}
	code, err := generateCheckinCode(bytes.NewReader([]byte{0, 1, 10, 15, 20, 25, 30, 31}))
	if err != nil || code != "01AFMSYZ" {
		t.Fatalf("independent symbols: %q %v", code, err)
	}
}

type failedRandomReader struct{ err error }

func (r failedRandomReader) Read([]byte) (int, error) { return 0, r.err }

func TestGenerateCheckinCodeRandomFailure(t *testing.T) {
	failure := errors.New("random source unavailable")
	for _, tt := range []struct {
		name   string
		reader io.Reader
		want   error
	}{
		{"empty", bytes.NewReader(nil), io.EOF},
		{"partial", strings.NewReader("1234567"), io.ErrUnexpectedEOF},
		{"source error", failedRandomReader{failure}, failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, err := generateCheckinCode(tt.reader)
			if code != "" || !errors.Is(err, tt.want) {
				t.Fatalf("code=%q err=%v, want empty code and %v", code, err, tt.want)
			}
		})
	}
}

package sopsage

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// TestGeneratePowerOfTwoAlphabet is a regression test for an infinite loop.
//
// Rejection sampling computed its cutoff as a byte. When the alphabet size
// divided 256 evenly -- 2, 4, 8, ... 64, 256, and 64 is a very common choice --
// the cutoff was 256, which truncates to 0. Every random draw was then above
// the cutoff and discarded, so generation spun forever with no timeout,
// hanging `init` on a release that declared such an alphabet.
func TestGeneratePowerOfTwoAlphabet(t *testing.T) {
	sizes := []int{2, 4, 8, 16, 32, 64, 128, 256}

	for _, size := range sizes {
		alphabet := buildAlphabet(size)
		require.Len(t, []rune(alphabet), size)

		done := make(chan string, 1)
		go func() {
			value, err := Generate(domain.Generator{
				Kind: domain.GeneratorPassword, Length: 32, Alphabet: alphabet,
			})
			if err != nil {
				done <- ""
				return
			}
			done <- value
		}()

		select {
		case value := <-done:
			require.Len(t, []rune(value), 32, "alphabet size %d", size)
			for _, r := range value {
				assert.True(t, strings.ContainsRune(alphabet, r),
					"alphabet size %d produced %q, which is outside the alphabet", size, r)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("generation with a %d-character alphabet did not terminate", size)
		}
	}
}

// buildAlphabet returns a distinct-rune alphabet of exactly n characters.
func buildAlphabet(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		// Start above the control range so every rune is printable-ish and,
		// more importantly, distinct.
		b.WriteRune(rune(0x0100 + i))
	}
	return b.String()
}

func TestGenerateRejectsUnusableAlphabets(t *testing.T) {
	_, err := Generate(domain.Generator{Kind: domain.GeneratorPassword, Length: 16, Alphabet: "a"})
	require.Error(t, err, "a single-character alphabet produces a constant, not a secret")

	_, err = Generate(domain.Generator{
		Kind: domain.GeneratorPassword, Length: 16, Alphabet: buildAlphabet(257),
	})
	require.Error(t, err, "an alphabet beyond 256 runes cannot be sampled from one byte")
}

func TestGenerateKinds(t *testing.T) {
	cases := []struct {
		kind   domain.GeneratorKind
		length int
		check  func(t *testing.T, value string)
	}{
		{domain.GeneratorHex, 64, func(t *testing.T, v string) {
			assert.Len(t, v, 64, "hex length is in output characters, not bytes")
			assert.NotContains(t, v, "g")
		}},
		{domain.GeneratorBase64, 32, func(t *testing.T, v string) {
			assert.NotEmpty(t, v)
		}},
		{domain.GeneratorUUID, 0, func(t *testing.T, v string) {
			assert.Len(t, v, 36)
			assert.Equal(t, byte('4'), v[14], "must be a v4 UUID")
		}},
		{domain.GeneratorAgeKey, 0, func(t *testing.T, v string) {
			assert.True(t, strings.HasPrefix(v, "AGE-SECRET-KEY-"))
		}},
	}

	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			value, err := Generate(domain.Generator{Kind: tc.kind, Length: tc.length})
			require.NoError(t, err)
			tc.check(t, value)
		})
	}
}

func TestGenerateRequiresAGenerator(t *testing.T) {
	_, err := Generate(domain.Generator{Kind: domain.GeneratorNone})
	require.Error(t, err, "a secret with no generator must be supplied by the operator")
	assert.Equal(t, domain.ExitSecrets, domain.ExitCode(err))
}

func TestGenerateIsNotDeterministic(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		value, err := Generate(domain.Generator{Kind: domain.GeneratorPassword, Length: 24})
		require.NoError(t, err)
		assert.False(t, seen[value], "generated the same secret twice")
		seen[value] = true
	}
}

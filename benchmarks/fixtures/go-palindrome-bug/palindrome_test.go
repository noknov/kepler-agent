package palindrome

import "testing"

func TestIsPalindrome(t *testing.T) {
	tests := map[string]bool{
		"":     true,
		"a":    true,
		"aba":  true,
		"abba": true,
		"abc":  false,
		"abca": false,
	}
	for input, want := range tests {
		if got := IsPalindrome(input); got != want {
			t.Fatalf("IsPalindrome(%q) = %v, want %v", input, got, want)
		}
	}
}

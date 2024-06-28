package soju

import (
	"testing"
)

func TestIsHighlight(t *testing.T) {
	nick := "SojuUser"
	testCases := []struct {
		name string
		text string
		hl   bool
	}{
		{"noContains", "hi there Soju User!", false},
		{"standalone", "SojuUser", true},
		{"middle", "hi there SojuUser!", true},
		{"start", "SojuUser: how are you doing?", true},
		{"end", "maybe ask SojuUser", true},
		{"inWord", "but OtherSojuUserSan is a different nick", false},
		{"startWord", "and OtherSojuUser is another different nick", false},
		{"endWord", "and SojuUserSan is yet a different nick", false},
		{"underscore", "and SojuUser_san has nothing to do with me", false},
		{"zeroWidthSpace", "writing S\u200BojuUser shouldn't trigger a highlight", false},
		{"url", "https://SojuUser.example", false},
		{"startURL", "https://SojuUser.example is a nice website", false},
		{"endURL", "check out my website: https://SojuUser.example", false},
		{"parenthesizedURL", "see my website (https://SojuUser.example)", false},
		{"afterURL", "see https://SojuUser.example (cc SojuUser)", true},
		{"twiceInURL", "https://SojuUser.example/bar/SojuUser/baz", false},
	}

	for _, tc := range testCases {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			hl := isHighlight(tc.text, nick)
			if hl != tc.hl {
				t.Errorf("isHighlight(%q, %q) = %v, but want %v", tc.text, nick, hl, tc.hl)
			}
		})
	}
}

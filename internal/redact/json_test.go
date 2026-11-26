package redact

import "testing"

func TestJSONEscapeOffsets(t *testing.T) {
	// Decoded: a, newline, b, @, c, emoji, d.
	content := `a\nb\u0040c\ud83d\ude00d`
	escapes, err := jsonEscapes(content)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		decoded int
		end     bool
		want    int
	}{
		{0, false, 0},
		{2, false, 3},  // 'b' follows the \n escape
		{3, true, 4},   // end of 'b'
		{4, false, 10}, // 'c' follows the \u0040 escape
		{4, true, 10},
		{5, false, 11}, // emoji surrogate pair starts
		{9, true, 23},  // surrogate pair ends
		{9, false, 23}, // 'd'
		{10, true, 24},
	}
	for _, c := range cases {
		if got := escapes.rawOffset(c.decoded, c.end); got != c.want {
			t.Errorf("rawOffset(%d, end=%v) = %d, want %d", c.decoded, c.end, got, c.want)
		}
	}
	decoded, err := decodeJSONString(content)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "a\nb@c\U0001F600d" {
		t.Fatalf("decoded = %q", decoded)
	}
}

package secrets

import "testing"

func TestMaskBasic(t *testing.T) {
	r := NewRegistry()
	r.Register("s3cret-value")
	got := r.Mask("Authorization: Bearer s3cret-value here")
	want := "Authorization: Bearer *** here"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMaskLongestFirst(t *testing.T) {
	r := NewRegistry()
	r.Register("abcd")
	r.Register("abcdefgh")
	got := r.Mask("prefix abcdefgh suffix")
	if got != "prefix *** suffix" {
		t.Fatalf("longest-first not honored: %q", got)
	}
}

func TestMaskIgnoresShort(t *testing.T) {
	r := NewRegistry()
	r.Register("ab")
	got := r.Mask("a fabulous day")
	if got != "a fabulous day" {
		t.Fatalf("short value should not be registered: %q", got)
	}
}

func TestMaskEmpty(t *testing.T) {
	r := NewRegistry()
	r.Register("longsecret")
	if r.Mask("") != "" {
		t.Fatal("empty input should stay empty")
	}
}

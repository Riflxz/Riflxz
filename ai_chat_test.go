package main

import "testing"

// ─── aiExtractPrompt ─────────────────────────────────────────────────────────

func TestAIExtractPrompt(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantOK  bool
	}{
		{"yuuki apa kabar", "apa kabar", true},
		{"yuki apa kabar", "apa kabar", true},
		{"Yuuki tolong bantu", "tolong bantu", true},
		{"YUKI, buatkan puisi", "buatkan puisi", true},
		{"yuuki", "", true},
		{"yuki!", "", true},
		{"yuuki?", "", true},
		{"halo yuuki apa kabar", "", false},
		{"yukii apa kabar", "", false},
		{"!yuuki apa kabar", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := aiExtractPrompt(c.in)
		if ok != c.wantOK || got != c.want {
			t.Errorf("aiExtractPrompt(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// ─── aiParseStream (port dari feelbetterEngine.js) ───────────────────────────

func TestAIParseStreamTextDelta(t *testing.T) {
	raw := "data: {\"type\":\"text-delta\",\"delta\":\"Halo\"}\n" +
		"data: {\"type\":\"text-delta\",\"delta\":\" Kak\"}\n" +
		"data: [DONE]\n"
	if got := aiParseStream([]byte(raw)); got != "Halo Kak" {
		t.Errorf("text-delta parse = %q, want %q", got, "Halo Kak")
	}
}

func TestAIParseStreamChoices(t *testing.T) {
	raw := "data: {\"choices\":[{\"delta\":{\"content\":\"Halo\"}}]}\n" +
		"data: {\"choices\":[{\"text\":\" dunia\"}]}\n"
	if got := aiParseStream([]byte(raw)); got != "Halo dunia" {
		t.Errorf("choices parse = %q, want %q", got, "Halo dunia")
	}
}

func TestAIParseStreamFallback(t *testing.T) {
	raw := "event: message\nid: 1\nretry: 1000\n\nHalo dunia\n"
	if got := aiParseStream([]byte(raw)); got != "Halo dunia" {
		t.Errorf("fallback parse = %q, want %q", got, "Halo dunia")
	}
}

func TestAIParseStreamContentText(t *testing.T) {
	raw := "data: {\"content\":\"A\"}\ndata: {\"text\":\"B\"}\n"
	if got := aiParseStream([]byte(raw)); got != "AB" {
		t.Errorf("content/text parse = %q, want %q", got, "AB")
	}
}
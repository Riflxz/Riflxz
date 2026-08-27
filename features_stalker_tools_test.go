package main

import (
	"testing"
)

const daFontFixture = `<a name="76747"></a><div class="lv1left dfbg"><span class="highlight">Horror</span> by <a href="rudairo-manase.d9767">Rudairo Manase</a></div><div class="lv1right dfbg">in <a href="mtheme.php?id=6">Script</a> &gt; <a href="theme.php?cat=605">Trash</a></div><div class="lv2right">&nbsp;<span class="light">74,923 downloads (51 yesterday)</span> &nbsp; <a class="tdn help black" style="cursor:help" target="_blank" href="./faq.php#copyright">100% Free</a></div><div class="dlbox"><a class="dl" title="32 K" href="//dl.dafont.com/dl/?f=horror"  rel="nofollow">&nbsp;Download&nbsp;</a></div><div class="lv1left dfbg">Horrors &euro; by Hawtpixel</div><div class="dlbox"><a class="dl" href="//dl.dafont.com/dl/?f=horrors"></a></div>`

func TestParseDaFont(t *testing.T) {
	items := parseDaFont(daFontFixture)
	if len(items) != 2 {
		t.Fatalf("harusnya 2 item, dapat %d", len(items))
	}
	if items[0].Name != "Horror" || items[0].Author != "Rudairo Manase" {
		t.Errorf("item 1 salah: %+v", items[0])
	}
	if items[0].Link != "https://dl.dafont.com/dl/?f=horror" {
		t.Errorf("link harus https: %s", items[0].Link)
	}
	// Entity &euro; harus ter-unescape.
	if items[1].Name != "Horrors €" {
		t.Errorf("unescape entity gagal: %q", items[1].Name)
	}
}

func TestParsePhoneJID(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"628123456789", "628123456789", true},
		{"+62 812-3456-789", "628123456789", true},
		{"08123456789", "628123456789", true},
		{"0812 3456 789", "628123456789", true},
		{"8123456789", "", false}, // tanpa 62/0 → tidak bisa dinormalisasi
		{"abc", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		jid := parsePhoneJID(c.in)
		if c.ok && jid.User != c.want {
			t.Errorf("%q → %q, want %q", c.in, jid.User, c.want)
		}
		if !c.ok && jid.User != "" {
			t.Errorf("%q → %q, want kosong", c.in, jid.User)
		}
	}
}

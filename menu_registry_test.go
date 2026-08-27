package main

import (
	"strings"
	"testing"
)

// ─── Validasi konsistensi registry ───────────────────────────────────────────

func TestRegistryNoDuplicateNames(t *testing.T) {
	seen := map[string]string{} // name → cat
	for _, c := range cmdRegistry {
		if prev, ok := seen[c.Name]; ok {
			t.Errorf("duplicate command name %q (cat %s vs %s)", c.Name, prev, c.Cat)
		}
		seen[c.Name] = c.Cat
	}
}

func TestRegistryCategoriesValid(t *testing.T) {
	valid := map[string]bool{}
	for _, cat := range categoryOrder {
		valid[cat] = true
	}
	for _, c := range cmdRegistry {
		if !valid[c.Cat] {
			t.Errorf("command %q pakai kategori %q yang tidak ada di categoryOrder", c.Name, c.Cat)
		}
	}
}

func TestRegistryCategoryEmojiComplete(t *testing.T) {
	for _, cat := range categoryOrder {
		if categoryEmoji[cat] == "" {
			t.Errorf("kategori %q tidak punya emoji", cat)
		}
	}
}

func TestRegistryOwnerCommandsHiddenFromPublic(t *testing.T) {
	pub := cmdsByCat(false, false, false)
	for _, c := range cmdRegistry {
		if c.Owner {
			for _, pc := range pub[c.Cat] {
				if pc.Name == c.Name {
					t.Errorf("command owner %q bocor ke menu publik", c.Name)
				}
			}
		}
	}
}

func TestRegistryCreatorCommandsHiddenFromOwner(t *testing.T) {
	owner := cmdsByCat(true, false, false)
	for _, c := range cmdRegistry {
		if c.Creator {
			for _, oc := range owner[c.Cat] {
				if oc.Name == c.Name {
					t.Errorf("command creator %q bocor ke menu owner biasa", c.Name)
				}
			}
		}
	}
}

func TestRegistryPremiumSeesPremiumCommandsOnly(t *testing.T) {
	prem := cmdsByCat(false, false, true)
	names := map[string]bool{}
	for _, cmds := range prem {
		for _, c := range cmds {
			names[c.Name] = true
		}
	}
	// Command Premium:true (play) harus tampil buat premium.
	if !names["play"] {
		t.Errorf("command premium \"play\" tidak tampil di menu premium")
	}
	// Command owner murni (addsender) TIDAK boleh bocor ke premium.
	if names["addsender"] {
		t.Errorf("command owner \"addsender\" bocor ke menu premium")
	}
}

// ─── Output menu ─────────────────────────────────────────────────────────────

func TestMenuTextContainsCategories(t *testing.T) {
	out := menuText(false, false, false)
	for _, cat := range visibleCats(false, false, false) {
		if !strings.Contains(out, strings.ToUpper(cat)) {
			t.Errorf("menuText tidak memuat kategori %q", cat)
		}
	}
	if !strings.Contains(out, "menucat") {
		t.Errorf("menuText harus menyarankan !menucat")
	}
}

func TestAllMenuTextContainsEveryPublicCommand(t *testing.T) {
	out := allMenuText(false, false, false)
	for _, c := range cmdRegistry {
		if c.Owner {
			continue
		}
		if !strings.Contains(out, "`"+Prefix+c.Name+"`") {
			t.Errorf("allMenuText tidak memuat command %q", c.Name)
		}
	}
}

func TestAllMenuTextOwnerSeesOwnerCommands(t *testing.T) {
	out := allMenuText(true, true, false)
	for _, c := range cmdRegistry {
		if !strings.Contains(out, "`"+Prefix+c.Name+"`") {
			t.Errorf("allMenuText (owner) tidak memuat command %q", c.Name)
		}
	}
}

func TestMenuCatText(t *testing.T) {
	out := menuCatText("fun", false, false, false)
	if !strings.Contains(out, "gempa") || !strings.Contains(out, "quran") {
		t.Errorf("menuCatText(fun) harus memuat gempa & quran, dapat: %q", out)
	}
}

func TestCatByKey(t *testing.T) {
	cases := map[string]string{
		"tools":    "tools",
		"TOOLS":    "tools",
		"🔧":        "tools",
		"konversi": "konversi",
		"sticker":  "sticker",
		"gakada":   "",
	}
	for key, want := range cases {
		if got := catByKey(key); got != want {
			t.Errorf("catByKey(%q) = %q, want %q", key, got, want)
		}
	}
}

package main

import "testing"

func TestDetectPasteLang(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			"js with leading comments (paste Gu8RZaqv)",
			"//Creator: JawirDev\n//Instagram: @jawirdesigner\n//Sumber: https://whatsapp.com/channel/0029Vb6Jcay8KMqestkMkj1L\n\n//Cek Stok Blox Fruits (Manual & Tunggal)\ncase 'bfstock':\ncase 'bloxfruitstock': {\n    await XReaction();\n    reply('🍓 Sedang mengambil data stok terbaru dari Blox Fruits...');\n",
			"javascript",
		},
		{
			"go",
			"package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"halo\")\n}\n",
			"go",
		},
		{
			"python",
			"import os\n\ndef main():\n    print(\"halo\")\n\nif __name__ == \"__main__\":\n    main()\n",
			"python",
		},
		{
			"json",
			"{\n  \"name\": \"yuuki\",\n  \"version\": \"1.0.0\"\n}\n",
			"json",
		},
		{
			"html",
			"<!DOCTYPE html>\n<html>\n<head><title>x</title></head>\n<body><div>halo</div></body>\n</html>\n",
			"html",
		},
		{
			"php",
			"<?php\n$nama = \"yuuki\";\necho $nama;\n",
			"php",
		},
		{
			"sql",
			"SELECT * FROM users WHERE id = 1;\n",
			"sql",
		},
		{
			"bash",
			"#!/bin/bash\nif [ -f /etc/passwd ]; then\n  echo \"ada\"\nfi\n",
			"bash",
		},
		{
			"dockerfile",
			"FROM node:20\nWORKDIR /app\nRUN npm install\nCMD [\"npm\", \"start\"]\n",
			"dockerfile",
		},
		{
			"yaml",
			"name: yuuki\nversion: 1.0\nservices:\n  - web\n",
			"yaml",
		},
		{
			"plain text",
			"Halo ini teks biasa\nTidak ada kode di sini\n",
			"",
		},
		{
			"cpp",
			"#include <iostream>\nusing namespace std;\nint main() {\n    cout << \"halo\" << endl;\n    return 0;\n}\n",
			"cpp",
		},
		{
			"java",
			"public class Main {\n    public static void main(String[] args) {\n        System.out.println(\"halo\");\n    }\n}\n",
			"java",
		},
		{
			"rust",
			"fn main() {\n    let mut x = 5;\n    println!(\"halo {}\", x);\n}\n",
			"rust",
		},
		{
			"css",
			".btn {\n  color: red;\n  background: blue;\n}\n@media (max-width: 600px) {\n  .btn { display: none; }\n}\n",
			"css",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectPasteLang(tc.content); got != tc.want {
				t.Errorf("detectPasteLang() = %q, want %q", got, tc.want)
			}
		})
	}
}

#!/usr/bin/env python3
"""Generate GitHub profile stats SVG from the GitHub API.

Renders two cards:
  - stats.svg       : total repos, stars, followers, following
  - top-langs.svg   : top languages by bytes across public repos

Runs inside GitHub Actions with GITHUB_TOKEN so it never hits the
unauthenticated rate limit and does not depend on any third-party service.
"""

import json
import os
import sys
import urllib.request
import urllib.parse

USERNAME = os.environ.get("GITHUB_USERNAME", "Riflxz")
TOKEN = os.environ.get("GITHUB_TOKEN", "")
API = "https://api.github.com"

OUT_DIR = os.path.join(os.path.dirname(__file__), "..", "profile")
OUT_DIR = os.path.abspath(OUT_DIR)

# Theme (tokyonight-ish, matches the rest of the profile)
BG = "#1a1b27"
CARD = "#1f2335"
TEXT = "#c0caf5"
ACCENT = "#7aa2f7"
MUTED = "#565f89"
GREEN = "#9ece6a"
YELLOW = "#e0af68"
RED = "#f7768e"


def api(path):
    url = API + path
    req = urllib.request.Request(url)
    req.add_header("Accept", "application/vnd.github+json")
    if TOKEN:
        req.add_header("Authorization", f"Bearer {TOKEN}")
    req.add_header("User-Agent", "riflxz-profile-stats")
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)


def esc(text):
    return str(text).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def rounded_rect(x, y, w, h, r, fill):
    return (
        f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" '
        f'fill="{fill}"/>'
    )


def stat_card():
    user = api(f"/users/{USERNAME}")
    repos = api(f"/users/{USERNAME}/repos?per_page=100&sort=updated")

    total_stars = sum(r.get("stargazers_count", 0) for r in repos)
    total_forks = sum(r.get("forks_count", 0) for r in repos)
    followers = user.get("followers", 0)
    following = user.get("following", 0)
    public_repos = user.get("public_repos", 0)

    # Card layout
    W, H = 480, 180
    pad = 24
    row_h = 36
    x1 = pad
    x2 = W // 2 + 10

    rows = [
        ("Repos", public_repos, ACCENT),
        ("Stars", total_stars, YELLOW),
        ("Followers", followers, GREEN),
        ("Following", following, RED),
    ]

    svg = []
    svg.append(
        f'<svg width="{W}" height="{H}" viewBox="0 0 {W} {H}" '
        f'fill="none" xmlns="http://www.w3.org/2000/svg">'
    )
    svg.append(f"<style>.t{{font:600 14px 'Segoe UI',Ubuntu,sans-serif;fill:{TEXT}}}"
               f".l{{font:600 12px 'Segoe UI',Ubuntu,sans-serif;fill:{MUTED}}}"
               f".v{{font:700 16px 'Segoe UI',Ubuntu,sans-serif}}</style>")
    svg.append(rounded_rect(0.5, 0.5, W - 1, H - 1, 8, CARD))
    svg.append(f'<text x="{pad}" y="34" class="t">GitHub Stats</text>')

    # Two columns of label/value pairs
    for i, (label, value, color) in enumerate(rows):
        col = i % 2
        row = i // 2
        x = x1 if col == 0 else x2
        y = 64 + row * row_h
        svg.append(f'<text x="{x}" y="{y}" class="l">{esc(label)}</text>')
        svg.append(
            f'<text x="{x}" y="{y + 22}" class="v" fill="{color}">{value}</text>'
        )

    svg.append("</svg>")
    return "".join(svg)


def lang_card():
    repos = api(f"/users/{USERNAME}/repos?per_page=100&sort=updated")
    lang_bytes = {}
    for repo in repos:
        if repo.get("fork"):
            continue
        langs = api(f"/repos/{USERNAME}/{repo['name']}/languages")
        for lang, size in langs.items():
            lang_bytes[lang] = lang_bytes.get(lang, 0) + size

    total = sum(lang_bytes.values()) or 1
    top = sorted(lang_bytes.items(), key=lambda kv: kv[1], reverse=True)[:6]

    colors = {
        "Go": "#00ADD8",
        "Python": "#3776AB",
        "JavaScript": "#f1e05a",
        "TypeScript": "#3178C6",
        "Shell": "#89e051",
        "HTML": "#e34c26",
        "CSS": "#563d7c",
        "Vue": "#41b883",
        "Dockerfile": "#384d54",
        "C": "#555555",
        "C++": "#f34b7d",
        "Java": "#b07219",
        "Rust": "#dea584",
    }

    W, H = 300, 46 + len(top) * 30
    svg = []
    svg.append(
        f'<svg width="{W}" height="{H}" viewBox="0 0 {W} {H}" '
        f'fill="none" xmlns="http://www.w3.org/2000/svg">'
    )
    svg.append(f"<style>.t{{font:600 14px 'Segoe UI',Ubuntu,sans-serif;fill:{TEXT}}}"
               f".l{{font:600 13px 'Segoe UI',Ubuntu,sans-serif;fill:{TEXT}}}"
               f".p{{font:600 12px 'Segoe UI',Ubuntu,sans-serif;fill:{MUTED}}}</style>")
    svg.append(rounded_rect(0.5, 0.5, W - 1, H - 1, 8, CARD))
    svg.append(f'<text x="24" y="30" class="t">Tech Stack</text>')

    y = 56
    for lang, size in top:
        pct = size / total * 100
        color = colors.get(lang, "#8b949e")
        bar_w = (W - 48) * (pct / 100)
        svg.append(f'<circle cx="24" cy="{y - 4}" r="5" fill="{color}"/>')
        svg.append(f'<text x="36" y="{y}" class="l">{esc(lang)}</text>')
        svg.append(
            f'<text x="{W - 24}" y="{y}" class="p" text-anchor="end">'
            f'{pct:.1f}%</text>'
        )
        svg.append(
            f'<rect x="36" y="{y + 8}" width="{bar_w:.1f}" height="4" '
            f'rx="2" fill="{color}"/>'
        )
        y += 30

    svg.append("</svg>")
    return "".join(svg)


def main():
    os.makedirs(OUT_DIR, exist_ok=True)
    with open(os.path.join(OUT_DIR, "stats.svg"), "w") as f:
        f.write(stat_card())
    with open(os.path.join(OUT_DIR, "tech-stack.svg"), "w") as f:
        f.write(lang_card())
    print("Generated stats.svg and tech-stack.svg")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:  # noqa: BLE001 - surface any failure to the log
        print(f"ERROR: {e}", file=sys.stderr)
        sys.exit(1)

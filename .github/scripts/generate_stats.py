#!/usr/bin/env python3
"""Generate GitHub profile stats SVG from the GitHub API.

Renders two cards:
  - stats.svg       : metric grid + top-repos bar chart
  - tech-stack.svg  : top languages with progress bars

Runs inside GitHub Actions with GITHUB_TOKEN so it never hits the
unauthenticated rate limit and does not depend on any third-party service.
"""

import json
import os
import sys
import urllib.request

USERNAME = os.environ.get("GITHUB_USERNAME", "Riflxz")
TOKEN = os.environ.get("GITHUB_TOKEN", "")
API = "https://api.github.com"

OUT_DIR = os.path.join(os.path.dirname(__file__), "..", "profile")
OUT_DIR = os.path.abspath(OUT_DIR)

# Theme (tokyonight, matches the rest of the profile)
CARD = "#1f2335"
CARD_ALT = "#24283b"
TEXT = "#c0caf5"
MUTED = "#565f89"
ACCENT = "#7aa2f7"
GREEN = "#9ece6a"
YELLOW = "#e0af68"
RED = "#f7768e"
PURPLE = "#bb9af7"
TRACK = "#2f3549"

FONT = "'Segoe UI',Ubuntu,sans-serif"


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
    return f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" fill="{fill}"/>'


def stat_card():
    user = api(f"/users/{USERNAME}")
    repos = api(f"/users/{USERNAME}/repos?per_page=100&sort=updated")

    total_stars = sum(r.get("stargazers_count", 0) for r in repos)
    total_forks = sum(r.get("forks_count", 0) for r in repos)
    followers = user.get("followers", 0)
    following = user.get("following", 0)
    public_repos = user.get("public_repos", 0)

    # Top repos by stars (exclude forks)
    top_repos = sorted(
        [r for r in repos if not r.get("fork")],
        key=lambda r: r.get("stargazers_count", 0),
        reverse=True,
    )[:5]
    max_stars = max([r.get("stargazers_count", 0) for r in top_repos] + [1])

    # Layout
    W = 480
    metrics = [
        ("Repos", public_repos, ACCENT),
        ("Stars", total_stars, YELLOW),
        ("Forks", total_forks, PURPLE),
        ("Followers", followers, GREEN),
    ]

    # Metric grid: 4 cells in a row
    cell_w = (W - 48) / 4
    metric_h = 64
    chart_top = 96
    chart_h = 24 + len(top_repos) * 26
    H = chart_top + chart_h + 16

    svg = []
    svg.append(
        f'<svg width="{W}" height="{H}" viewBox="0 0 {W} {H}" '
        f'fill="none" xmlns="http://www.w3.org/2000/svg">'
    )
    svg.append(
        f"<style>"
        f".t{{font:700 15px {FONT};fill:{TEXT}}}"
        f".m{{font:700 22px {FONT}}}"
        f".ml{{font:600 11px {FONT};fill:{MUTED}}}"
        f".r{{font:600 12px {FONT};fill:{TEXT}}}"
        f".rs{{font:600 12px {FONT};fill:{YELLOW}}}"
        f"</style>"
    )
    svg.append(rounded_rect(0.5, 0.5, W - 1, H - 1, 12, CARD))

    # Header
    svg.append(f'<text x="24" y="34" class="t">GitHub Stats</text>')

    # Metric cells
    for i, (label, value, color) in enumerate(metrics):
        x = 24 + i * cell_w
        svg.append(rounded_rect(x, 48, cell_w - 8, metric_h, 8, CARD_ALT))
        svg.append(f'<text x="{x + 12}" y="{metric_h + 48 - 16}" class="ml">{esc(label)}</text>')
        svg.append(
            f'<text x="{x + 12}" y="{metric_h + 48 - 34}" class="m" fill="{color}">{value}</text>'
        )

    # Chart header
    svg.append(f'<text x="24" y="{chart_top + 18}" class="t">Top Repos</text>')

    # Bar chart
    y = chart_top + 40
    bar_max_w = W - 48 - 60
    for repo in top_repos:
        name = repo["name"]
        stars = repo.get("stargazers_count", 0)
        bar_w = (bar_max_w * stars / max_stars) if max_stars else 0
        svg.append(f'<text x="24" y="{y}" class="r">{esc(name)}</text>')
        svg.append(
            f'<text x="{W - 24}" y="{y}" class="rs" text-anchor="end">★ {stars}</text>'
        )
        svg.append(rounded_rect(24, y + 6, bar_max_w, 6, 3, TRACK))
        if bar_w > 0:
            svg.append(rounded_rect(24, y + 6, bar_w, 6, 3, ACCENT))
        y += 26

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

    W = 300
    H = 46 + len(top) * 32
    svg = []
    svg.append(
        f'<svg width="{W}" height="{H}" viewBox="0 0 {W} {H}" '
        f'fill="none" xmlns="http://www.w3.org/2000/svg">'
    )
    svg.append(
        f"<style>"
        f".t{{font:700 15px {FONT};fill:{TEXT}}}"
        f".l{{font:600 13px {FONT};fill:{TEXT}}}"
        f".p{{font:600 12px {FONT};fill:{MUTED}}}"
        f"</style>"
    )
    svg.append(rounded_rect(0.5, 0.5, W - 1, H - 1, 12, CARD))
    svg.append(f'<text x="24" y="30" class="t">Tech Stack</text>')

    y = 58
    bar_max_w = W - 48
    for lang, size in top:
        pct = size / total * 100
        color = colors.get(lang, "#8b949e")
        bar_w = bar_max_w * (pct / 100)
        svg.append(f'<circle cx="24" cy="{y - 4}" r="5" fill="{color}"/>')
        svg.append(f'<text x="36" y="{y}" class="l">{esc(lang)}</text>')
        svg.append(
            f'<text x="{W - 24}" y="{y}" class="p" text-anchor="end">'
            f'{pct:.1f}%</text>'
        )
        svg.append(rounded_rect(36, y + 8, bar_max_w, 6, 3, TRACK))
        if bar_w > 0:
            svg.append(rounded_rect(36, y + 8, bar_w, 6, 3, color))
        y += 32

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

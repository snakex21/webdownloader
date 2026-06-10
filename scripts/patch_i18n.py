#!/usr/bin/env python3
"""
Add new translation keys to every language in web/i18n.json. Missing
values fall back to the English defaults. Existing translations are
preserved untouched.

Run from the project root:  python scripts\patch_i18n.py
"""

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
I18N = ROOT / "web" / "i18n.json"

# New keys with their English defaults. Per-language overrides below
# (only for languages the project ships in the screenshot / README).
NEW_KEYS = {
    "advancedLabel": "Advanced options",
    "modeLabel": "Download mode",
    "modeFast": "Fast",
    "modeBrowser": "Accurate (Chromium)",
    "modeHint": "Fast mode uses plain HTTP. Accurate mode renders the page in a browser (Edge or Chrome required).",
    "includeSubdomains": "Include subdomains",
    "includeExternal": "Include external / CDN assets",
    "maxPagesLabel": "Max pages (0 = unlimited)",
    "maxTotalLabel": "Max total size (MB)",
    "maxFileLabel": "Max file size (MB)",
    "btnCancel": "Cancel",
    "statusTitle.cancelled": "Cancelled",
    "log.cancelled": "Cancelled by user",
    "stat.bytes": "Size",
}

# Per-language overrides (Polish is the primary UI language in this app).
PL_OVERRIDES = {
    "advancedLabel": "Zaawansowane opcje",
    "modeLabel": "Tryb pobierania",
    "modeFast": "Szybki",
    "modeBrowser": "Dokładny (Chromium)",
    "modeHint": "Szybki tryb używa zwykłego HTTP. Tryb Dokładny renderuje stronę w przeglądarce (wymaga Edge lub Chrome).",
    "includeSubdomains": "Pobieraj z subdomen",
    "includeExternal": "Pobieraj assety z CDN / zewnętrznych domen",
    "maxPagesLabel": "Maks. stron (0 = bez limitu)",
    "maxTotalLabel": "Maks. rozmiar łącznie (MB)",
    "maxFileLabel": "Maks. rozmiar pliku (MB)",
    "btnCancel": "Anuluj",
    "statusTitle.cancelled": "Anulowano",
    "log.cancelled": "Anulowano przez użytkownika",
    "stat.bytes": "Rozmiar",
}

# A few more languages worth hand-tuning.
OVERRIDES = {
    "de": {
        "advancedLabel": "Erweiterte Optionen",
        "modeLabel": "Download-Modus",
        "modeFast": "Schnell",
        "modeBrowser": "Genau (Chromium)",
        "modeHint": "Der Schnellmodus nutzt einfaches HTTP. Der genaue Modus rendert die Seite in einem Browser (Edge oder Chrome erforderlich).",
        "includeSubdomains": "Subdomains einschließen",
        "includeExternal": "Externe / CDN-Assets einschließen",
        "maxPagesLabel": "Max. Seiten (0 = unbegrenzt)",
        "maxTotalLabel": "Max. Gesamtgröße (MB)",
        "maxFileLabel": "Max. Dateigröße (MB)",
        "btnCancel": "Abbrechen",
        "statusTitle.cancelled": "Abgebrochen",
        "log.cancelled": "Vom Benutzer abgebrochen",
        "stat.bytes": "Größe",
    },
    "es": {
        "advancedLabel": "Opciones avanzadas",
        "modeLabel": "Modo de descarga",
        "modeFast": "Rápido",
        "modeBrowser": "Preciso (Chromium)",
        "modeHint": "El modo rápido usa HTTP simple. El modo preciso renderiza la página en un navegador (se necesita Edge o Chrome).",
        "includeSubdomains": "Incluir subdominios",
        "includeExternal": "Incluir recursos externos / CDN",
        "maxPagesLabel": "Máx. páginas (0 = sin límite)",
        "maxTotalLabel": "Tamaño total máx. (MB)",
        "maxFileLabel": "Tamaño máx. por archivo (MB)",
        "btnCancel": "Cancelar",
        "statusTitle.cancelled": "Cancelado",
        "log.cancelled": "Cancelado por el usuario",
        "stat.bytes": "Tamaño",
    },
    "fr": {
        "advancedLabel": "Options avancées",
        "modeLabel": "Mode de téléchargement",
        "modeFast": "Rapide",
        "modeBrowser": "Précis (Chromium)",
        "modeHint": "Le mode rapide utilise HTTP simple. Le mode précis rend la page dans un navigateur (Edge ou Chrome requis).",
        "includeSubdomains": "Inclure les sous-domaines",
        "includeExternal": "Inclure les ressources externes / CDN",
        "maxPagesLabel": "Max pages (0 = illimité)",
        "maxTotalLabel": "Taille totale max (Mo)",
        "maxFileLabel": "Taille max par fichier (Mo)",
        "btnCancel": "Annuler",
        "statusTitle.cancelled": "Annulé",
        "log.cancelled": "Annulé par l'utilisateur",
        "stat.bytes": "Taille",
    },
    "uk": {
        "advancedLabel": "Розширені параметри",
        "modeLabel": "Режим завантаження",
        "modeFast": "Швидкий",
        "modeBrowser": "Точний (Chromium)",
        "modeHint": "Швидкий режим використовує звичайний HTTP. Точний режим рендерить сторінку в браузері (потрібен Edge або Chrome).",
        "includeSubdomains": "Включаючи піддомени",
        "includeExternal": "Включаючи зовнішні / CDN ресурси",
        "maxPagesLabel": "Макс. сторінок (0 = без обмежень)",
        "maxTotalLabel": "Макс. загальний розмір (МБ)",
        "maxFileLabel": "Макс. розмір файлу (МБ)",
        "btnCancel": "Скасувати",
        "statusTitle.cancelled": "Скасовано",
        "log.cancelled": "Скасовано користувачем",
        "stat.bytes": "Розмір",
    },
    "ru": {
        "advancedLabel": "Расширенные параметры",
        "modeLabel": "Режим загрузки",
        "modeFast": "Быстрый",
        "modeBrowser": "Точный (Chromium)",
        "modeHint": "Быстрый режим использует обычный HTTP. Точный режим рендерит страницу в браузере (нужен Edge или Chrome).",
        "includeSubdomains": "Включая поддомены",
        "includeExternal": "Внешние / CDN ресурсы",
        "maxPagesLabel": "Макс. страниц (0 = без лимита)",
        "maxTotalLabel": "Макс. общий размер (МБ)",
        "maxFileLabel": "Макс. размер файла (МБ)",
        "btnCancel": "Отмена",
        "statusTitle.cancelled": "Отменено",
        "log.cancelled": "Отменено пользователем",
        "stat.bytes": "Размер",
    },
}

OVERRIDES["pl"] = PL_OVERRIDES


def main() -> int:
    data = json.loads(I18N.read_text(encoding="utf-8"))

    for lang, table in data.items():
        if not isinstance(table, dict):
            continue
        overrides = OVERRIDES.get(lang, {})
        for key, default in NEW_KEYS.items():
            if key in table and table[key]:
                # Don't overwrite an existing translation.
                continue
            table[key] = overrides.get(key, default)

    I18N.write_text(
        json.dumps(data, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    print(f"Patched {I18N} with {len(NEW_KEYS)} new keys for {len(data)} languages")
    return 0


if __name__ == "__main__":
    sys.exit(main())

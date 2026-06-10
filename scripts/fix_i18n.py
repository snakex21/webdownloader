#!/usr/bin/env python3
"""Add missing `checkboxRemember` key to all languages in web/i18n.json."""

import json
from pathlib import Path

PATH = Path("web/i18n.json")

TRANSLATIONS = {
    "pl": "Zapamiętaj opcje",
    "en": "Remember options",
    "de": "Optionen merken",
    "fr": "Enregistrer les options",
    "es": "Recordar opciones",
    "it": "Ricorda opzioni",
    "pt": "Lembrar opções",
    "ru": "Запомнить настройки",
    "uk": "Запам'ятати налаштування",
    "cs": "Zapamatovat nastavení",
    "sk": "Zapamätať nastavenia",
    "hu": "Beállítások megjegyzése",
    "ro": "Salvează opțiunile",
    "nl": "Opties onthouden",
    "sv": "Kom ihåg inställningar",
    "da": "Husk indstillinger",
    "fi": "Muista asetukset",
    "el": "Αποθήκευση επιλογών",
    "tr": "Seçenekleri hatırla",
    "ar": "تذكر الخيارات",
    "he": "שמור הגדרות",
    "ja": "設定を保存",
    "ko": "옵션 저장",
    "zh": "记住选项",
    "hi": "विकल्प याद रखें",
    "id": "Ingat opsi",
    "vi": "Ghi nhớ tùy chọn",
    "th": "จดจำตัวเลือก",
    "no": "Husk innstillinger",
    "bg": "Запомни настройките",
    "hr": "Zapamti postavke",
    "ca": "Recorda les opcions",
}

with PATH.open("r", encoding="utf-8") as f:
    data = json.load(f)

added = []
for code, value in TRANSLATIONS.items():
    if code in data and "checkboxRemember" not in data[code]:
        data[code]["checkboxRemember"] = value
        added.append(code)

# For languages we don't have, add English fallback
for code in data:
    if "checkboxRemember" not in data[code]:
        data[code]["checkboxRemember"] = "Remember options"

with PATH.open("w", encoding="utf-8") as f:
    json.dump(data, f, ensure_ascii=False, indent=2)
    f.write("\n")

print(f"Added checkboxRemember to: {', '.join(added) or 'none (already present)'}")
print(f"Total languages: {len(data)}")

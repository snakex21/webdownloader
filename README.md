# WebDownloader

Repo ma teraz z powrotem kilka trybow pracy.

## Tryby

- `Electron` - desktopowa aplikacja z GUI
- `Python CLI` - prosty downloader dowolnej strony
- `Express Web` - prosty interfejs w przegladarce do odpalania downloadera Python

## Uruchomienie desktop app

```bat
npm install
npm start
```

albo kliknij `run-electron.bat`.

## Uruchomienie web GUI

```bat
npm install
npm run web
```

potem wejdz na `http://localhost:3000`.

## Uruchomienie Python CLI

Wymagania:
- Python 3.x
- requests
- beautifulsoup4

Instalacja:

```bat
pip install requests beautifulsoup4
```

Uzycie:

```bat
run.bat <URL> [folder_wyjsciowy] [glebokosc]
```

Przyklady:

```bat
run.bat https://example.com
run.bat https://example.com output 2
```

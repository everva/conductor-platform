# ADR-0015 — macOS deployment / runtime (launchd gerçeği)

## Bağlam
Review (K4): plan "host'ta tek Go binary, kopyala-çalıştır" diyor ama macOS'te launchd daemon gerçeği
tamamen atlanmış. xirigo bu mayın tarlasını acı çekerek çözmüş (`conductor-tick.sh` + `com.xirigo.conductor.plist`);
plan bunları içselleştirmemiş.

## Karar
Faz-1a'dan itibaren, xirigo deseni birebir port edilir:
- **FDA (Full Disk Access):** launchd daemon `~/Documents/...` repolarına erişmek için **FDA-scoped binary**
  (genel `/bin/bash`'e FDA VERME — tüm script'leri etkiler; xirigo özel `conductor-bash` kopyası kullanmış).
- **Auth — keychain launchd'den ULAŞILMAZ:** `claude setup-token` ile long-lived OAuth token üret →
  `~/.conductor/oauth-token` (0600) → `CLAUDE_CODE_OAUTH_TOKEN` export. Reboot/keychain-lock'a dayanır.
  (subscription `claude -p`, ANTHROPIC_API_KEY YOK.)
- **PATH:** launchd boş PATH ile başlar → plist'te explicit PATH (`~/.local/bin`, gh, node, pnpm, git).
- **caffeinate:** tick boyunca `caffeinate -dimsu -w $$` (Mac uyursa iş durur).
- **launchd:** `RunAtLoad` + `KeepAlive` + `StartInterval` (poll).
- **Preflight check:** başlangıçta — `claude -p` çalışıyor mu, `gh auth` var mı, (1b) Postgres erişilir mi;
  değilse DUR + bildir (sessiz başarısızlık yok).

## Gerekçe
Yeniden icat edilecek bir şey değil — xirigo'da yaşanmış-çözülmüş. "Tek binary kopyala-çalıştır" iddiası
macOS'te yanlış; bu dersler alınmazsa daemon ilk koşuda FDA-reddi / boş-PATH / auth-yok ile çöker.

## Sonuç
- gh-token credential-helper (ADR-0004/0017) ile bu auth katmanı tutarlı.
- Faz-2 çok-host'ta her host bu runtime temeline sahip olmalı.

## Durum
✅ Kapandı (K4).

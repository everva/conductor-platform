# ADR-0043 — Native multi-file diff editor for many-file task reviews (Faz-P / P2c)

## Bağlam
Kullanıcı canlı demo'da gerçek bir UX boşluğu yakaladı: "burada 10 adet diff olduğunu düşün, ben
bunları nasıl tek tek kontrol edeceğim?" P2a/P2b'de çok-dosyalı bir task diff'i `runShowDiff`/
"Open Diff" ile açıldığında **dosya başına quick-pick** sunuyordu (aç → dosya seç → kapat → tekrar).
2 dosya için kabul edilebilir; 10 dosya için ZAHMETLİ — bir PR'ı tek tek dosya seçerek incelemek gibi.

VS Code'un built-in **multi-file diff editörü** (SCM "Open All Changes"'in kullandığı yüzey) tüm
değişen dosyaları TEK kaydırılabilir sekmede, her biri native diff (red/green, F7 ile sonraki
değişiklik, dosya başlıkları collapse) gösterir = GitHub-PR / Devin review ekranı. İstenen bu.

## Karar
**Çok-dosyalı + interactive diff açılışı → native multi-file diff editörü (`openMultiFileDiff`).**
- `openStoredDiff`'in interactive çok-dosya dalı artık `_workbench.openMultiDiffEditor`'ı (built-in
  multi-diff yüzeyi) AYNI `conductor-diff:` before/after side URI'leriyle açar — her dosya için
  `{originalUri, modifiedUri}` resource'u; başlık `<task> — N files`. P2b full-file upgrade ZATEN
  `entry.files`'a uygulandığından her pane tam-dosya gösterir.
- **Güvenli fallback:** komut yoksa/throw ederse `openMultiFileDiff` false döner → eski **per-file
  quick-pick**'e düşer (eski host'larda da çalışır). `_workbench.*` internal komut riski böyle
  sınırlandı.
- Tek dosya → tek `vscode.diff` (değişmedi). Deep-link split (`openTaskDiffBeside`, non-interactive)
  → birincil tek dosya beside (değişmedi; split tek-diff'lik bir yüzey).

## Sonuçlar
- **P2c (bu commit):** yalnız `editor/` (`openMultiFileDiff` + `openStoredDiff` dalı + testler).
  Backend/web DOKUNULMADI; token DONMUŞ (URI'ler token-free store'u key'ler). Yeni komut/contribution
  yok — mevcut "Open Diff"/showDiff/right-click yolları otomatik multi-file editöre yükseldi.
- **Doğrulama:** editör gate (typecheck×2 + eslint-0 + **vitest 244/3**: multi-diff editör resources +
  fallback-to-quick-pick + tek-dosya/binary yolları korunur + esbuild) + **electron smoke YEŞİL**
  (1.125.1, exit 0). Multi-diff komutunun GERÇEK render'ı = canlı fork + çok-dosyalı seeded diff
  (kullanıcı görsel onayı; smoke'ta diff verisi yok).
- **GÖRSEL capstone (kullanıcı):** canlı fork'ta çok-dosyalı bir task diff aç → tüm dosyalar tek
  scrollable native diff editöründe.
- ADR-0030 / ADR-0040 (P2a) / ADR-0041 (P2b) KORUNUR. Olası ileri-iş: session-view DIFF dosya
  satırlarını tıklanabilir yap (her satır → o dosyanın native diff'i) — webview tarafı, follow-up.

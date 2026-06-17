# ADR-0007 — Platform dili: Go

## Bağlam
Çekirdek (provisioner + registry + event-stream + governance + resource-governor) hangi dille? Hedef:
**her sunucuya kolay kurulum** + çok-host + dayanıklı uzun-çalışan daemon. Adaylar: Go / Python / TS.

## Karar
**Go.** Çekirdek tek statik binary. develop/verify motorları (bash/Python) ayrı **subprocess** olarak
sürülür. LLM = `claude -p` **subprocess** (subscription, API-key yok → Go'da AI-SDK gerekmez).

**Şema codegen:** Faz-3 UI'si (VS Code/web) TS zorunlu. Event/state şeması Go struct olarak DEĞİL,
**tek-kaynak (JSON Schema veya protobuf)** tanımlanır → hem Go hem TS'e codegen. Şema tek yerde yaşar.

## Gerekçe
- Tek binary → kopyala-çalıştır; Python (venv/sürüm) ve Node (node_modules) kurulum derdini sıfırlar. Çok-host için gerçek kazanç.
- Cross-compile (linux/mac, amd64/arm64) tek komut.
- goroutine → provisioner+governor+event-stream için ideal eşzamanlılık.
- LLM subprocess olduğu için Go'nun AI-ekosistem eksikliği bizi etkilemez.
- Sentinel/xirigo'nun Python/bash araçları subprocess kalır — yeniden yazılmaz.

## Sonuç
- Şema tek-kaynak disiplini baştan kurulmalı (Go+TS tip drift'ini önler).
- Operasyonel araçlar (systemd/launchd unit, PATH) Go binary'i çevreler.

## Güncelleme (ADR-0010)
Merkezi **Postgres** bağımlılığı eklendi (registry+lease için). "Sıfır bağımlılık" iddiası → "host'ta sıfır
bağımlılık" olarak korunur: topoloji = **merkezi tek Postgres + her host'ta ince Go client binary**. Host'a
Postgres kurulmaz; tek-binary kurulum kolaylığı host tarafında geçerli kalır.

## Durum
✅ Kapandı (ADR-0010 ile rafine edildi).

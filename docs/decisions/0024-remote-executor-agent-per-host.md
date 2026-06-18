# ADR-0024 — Remote-executor modeli: agent-per-host (2B-0)

## Bağlam
Faz-2 çok-host gerçek ihtiyaç (ADR-0008: iOS-build Mac'te; capability routing). 2B-0 kararı: tick'in develop'ı
uzak host'ta nasıl koşar? İki seçenek: (A) merkezi orchestrator hedef host'a **ssh ile komut** koşturur; (B)
her host'ta **agent** (= aynı conductor daemon) çalışır, merkezi PG'den kendi yeteneklerine uyan lease'i alır,
işi YERELDE koşturur. Kullanıcı kararı (2026-06-18): **(B) agent-per-host.**

## Karar
**Remote-executor = agent-per-host: conductor daemon HER host'ta çalışır.**
- **Self-registration:** daemon başlangıçta merkezi PG'ye host kaydı yapar (`host_id` + `capabilities`, ör.
  Mac: `[ios-build, macos, web]`, Linux: `[linux, web, backend]`) + periyodik host-heartbeat.
- **Capability-routing (pull):** daemon yalnız `lane.requires ⊆ self.capabilities` olan task'ları PickReady ile
  alır (lane→requires `lanes` tablosunda; ADR-0008). iOS-lane yalnız `ios-build` yetenekli host'a düşer.
- **Host-üstü "repo başına 1":** ZATEN garanti — Postgres lease (N-4: PK `project_id` + `ON CONFLICT DO NOTHING`)
  host-üstü atomik. İki host aynı repoya yazamaz; ek mekanizma gerekmez.
- **İş yerelde koşar:** her host kendi provisioner/clone/worktree/develop/verify/merge'ini YERELDE yürütür
  (Faz-1 daemon olduğu gibi), state+event'i merkezi PG'ye yazar. **Cross-host komut-exec YOK, ssh YOK, uzak
  workspace-sync YOK.**
- **Güvenlik:** her host yalnız kendi işini kendi kimlik bilgileriyle (gh-token, claude oauth) koşar; hiçbir host
  başka host'ta komut çalıştırmaz. Tek paylaşılan yüzey merkezi PG (+ opsiyonel remote-push, ADR-0022).
- **Deploy:** her host'a daemon (Docker/k8s — P4; Mac native, iOS-build için). Merkez yok = SPOF yok (PG dışında).

## Gerekçe
- **İnşa ettiğimiz daemon ZATEN per-host tick-runner** (merkezi-PG state + host-üstü lease + yerel pipeline). Çok-host
  = "aynı daemon'ı her host'ta çalıştır" + capability-aware lease → **minimum yeni kod**, refactor değil.
- ssh-modeli: auth/workspace-sync karmaşası, kırılganlık, merkez-bottleneck/SPOF, yerel-tick modelini atar. Agent
  modeli xirigo lease desenine ve mevcut mimariye doğal oturur.
- "Remote-executor" = uzak-komut DEĞİL; **agent daemon'dır, iş ait olduğu host'ta koşar.**

## Sonuç — Dalga B kapsamı (bu kararla sadeleşti)
- **2B-1:** host registry + capabilities (`hosts`+`lanes` tabloları, migration; daemon self-register + host-heartbeat).
- **2B-2:** capability routing (PickReady `requires ⊆ capabilities` filtresi; lane→requires).
- **2B-3:** agent-as-daemon kanıtı — iki daemon/tek-PG: host-üstü lease (iki host aynı repoya yazamaz) +
  capability routing (iOS-lane yalnız Mac-yetenekli agent'a) yerel-simüle (2 daemon, 2 host_id) ile kanıtlanır.
- **2B-4:** host-başına kaynak cap (governor per-host; Mac≠Linux kapasite).

## Durum
🔄 Uygulanıyor (Dalga B). ssh remote-executor REDDEDİLDİ.

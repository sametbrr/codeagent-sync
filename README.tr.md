[![CI](https://github.com/sametbrr/codeagent-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/sametbrr/codeagent-sync/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](go.mod)

# codeagent-sync

Claude Code ve Codex yapılandırmasını — talimatlar, ayarlar, skill'ler, agent'lar, kancalar, MCP sunucuları ve plugin listeleri — her makinede aynı tutar, uçtan uca şifreli.

> 🇬🇧 For English see [README.md](README.md)

[Hızlı Başlangıç](#hızlı-başlangıç) • [Kurulum](#kurulum) • [Kullanım](#kullanım) • [Neler Senkronlanır](#neler-senkronlanır) • [Sorun Giderme](#sorun-giderme) • [Güvenlik](#güvenlik)

---

## Hızlı Başlangıç

```bash
npm install -g codeagent-sync   # or: pnpm add -g codeagent-sync
codeagent-sync init             # storage, passphrase, first sync
```

Diğer her makinede: kurulu bir makinede `codeagent-sync join-code`, yeni makinede `codeagent-sync init --join <kod>`.

---

## Özellikler

- **İki araç, tek düzen** — `~/.claude`, `~/.claude.json` (yalnız MCP sunucuları), `~/.codex` ve iki aracın da okuduğu `~/.agents/skills` altındaki skill'ler
- **Uçtan uca şifreli** — her dosya makineden çıkmadan önce [age](https://age-encryption.org) ile şifrelenir; depolama yalnız dosya adlarını ve boyutlarını görür
- **Üzerine yazmaz, birleştirir** — ayar dosyaları öğe öğe birleşir; iki makine farklı ayarları değiştirdiğinde çakışma olmaz, `config.toml` içindeki yorumlar korunur
- **Taşınabilir yollar** — ev dizini yolları çevrilir; kullanıcı adı ya da işletim sistemi farklı makineler kendi yollarını alır
- **Araçlar arası paylaşım** — bir araçtaki skill ya da MCP sunucusunun diğerinde çalışıp çalışmayacağını değerlendirir, onaylarsan paylaştırır
- **Otomatik** — kancalar arka planda senkronlar, çakışmaları ve paylaşılabilecek yenilikleri ajana bir kez bildirir
- **Neyin senkronlanacağını sen seçersin** — tüm makineler ya da tek makine için include ve exclude kuralları, tek bir ayara kadar
- **Makine envanteri** — araçların ve MCP programlarının diğer makinelerde nasıl kurulduğunu ve bu makinede neyin eksik olduğunu gösterir
- **Güvenli** — her değişiklik için yedek ve `undo`, çakışmada üzerine yazmak yerine kenara ayırma, yarışlara karşı koşullu yazma
- **Depolama** — Cloudflare R2, Amazon S3 ve S3 uyumlu servisler, Google Cloud Storage, WebDAV

---

## Gereksinimler

- macOS, Linux ya da Windows
- Claude Code ve/veya Codex (bir makinede kurulu olmayan araca orada dokunulmaz)
- Cloudflare R2, S3, GCS ya da bir WebDAV sunucusunda bir kova ve ona okuma-yazma yetkisi olan kimlik bilgileri
- npm ya da pnpm ile kurmak için Node.js 18 ya da üzeri (veya hazır ikili dosya, ya da kaynaktan derlemek için Go 1.24 ve üzeri)

---

## Kurulum

```bash
npm install -g codeagent-sync
pnpm add -g codeagent-sync
```

Paket, sistemine uygun programı sürümün checksum'larıyla doğrulayıp `~/.local/bin/codeagent-sync` konumuna kurar (Windows'ta `%USERPROFILE%\.local\bin`). pnpm kurulum betiklerini varsayılan olarak çalıştırmaz; o durumda program ilk çalıştırmada kurulur. Her makinede aynı yerde tut: otomatik senkronun kancaları programı oradan başlatır. `codeagent-sync update` onu en son sürümle değiştirir.

<details>
<summary><strong>Hazır ikili dosyalar ve kaynaktan derleme</strong></summary>

macOS, Linux ve Windows için ikili dosyalar [releases](https://github.com/sametbrr/codeagent-sync/releases) sayfasında: sistemine uygun olanın adını `codeagent-sync` yapıp `~/.local/bin` içine koy.

Kaynaktan derlemek için (Go 1.24 ya da üzeri):

```bash
git clone https://github.com/sametbrr/codeagent-sync && cd codeagent-sync
make install
```

</details>

---

## Kullanım

### İlk makineyi kurmak

```bash
codeagent-sync init
```

`init` depolamayı ve bir parola sorar, kimlik bilgileri izin veriyorsa kovayı oluşturur, neyi yükleyeceğini listeleyip onay ister, sonra otomatik senkronu önerir. Parola kurtarılamaz: parola yöneticine kaydet. Parola yerine bir age anahtar dosyası da kullanabilirsin (`--key-file`).

### Başka bir makine eklemek

```bash
codeagent-sync join-code                 # on a machine that is set up
codeagent-sync init --join cas1-…        # on the new machine, same passphrase
```

Kod, depolama ayarlarını kovanın parolasıyla şifreli taşır. Bir makinenin ilk senkronunda yalnız o makinede olan dosyalar senin onayınla yüklenir; diğer makinelerden gelen dosyalar yedeklenerek yazılır.

### Günlük kullanım

```bash
codeagent-sync sync        # download others' changes, upload this machine's
codeagent-sync status      # what a sync would do, changing nothing
codeagent-sync conflicts   # versions set aside; settle with: conflicts resolve <path> --keep local|remote
codeagent-sync undo        # take back a change; undo --list shows them all
```

`pull` ve `push` yalnız tek yönde senkronlar. `sync`, `status`, `scan`, `conflicts`, `doctor`, `paths`, `machines` ve `auto status` `--json` ile JSON çıktı verir.

### Otomatik senkron

```bash
codeagent-sync auto enable
```

Claude Code ve Codex'e kancalar ekler: oturum başladığında ve her yanıttan sonra arka planda senkronlar; bilmen gerekenleri — bir çakışma, paylaşılabilecek yeni bir şey, başka makinede olan bir program — bir sonraki mesajında ajana bir kez iletir. Kancalar senkronlanan ayarların parçasıdır, diğer makinelerine de ulaşır. Codex yeni kancaları ancak onlara güvendiğinde çalıştırır: her makinede bir kez Codex'te `/hooks` yaz.

### Neyin senkronlanacağını seçmek

```bash
codeagent-sync paths                                    # what syncs, and the rules
codeagent-sync paths include claude/plans/**            # sync more
codeagent-sync paths exclude claude/look-again/**       # sync less
codeagent-sync paths exclude claude-state/.claude.json  # not Claude Code's MCP servers
codeagent-sync paths exclude claude/settings.json#permissions --local
codeagent-sync paths reset claude/plans/**              # drop a rule
```

Kurallar `<kök>/<kalıp>` biçimindedir (kökler: `claude`, `codex`, `agents`, `claude-state`, `home`, `codeagent`). Senkronlanan `~/.codeagent-sync/sync.yaml` dosyasında durur, böylece her makine aynı kurala uyar; `--local` ile yalnız o makine için `sync.local.yaml` dosyasına yazılır. Dışarı alınan yol senkronlanmayı bırakır ama hiçbir yerde silinmez. `#` işaretinden sonra bir exclude, bir ayar dosyasının öğelerini adlandırır — `settings.json` anahtarları, `mcp_servers.*` gibi `config.toml` tabloları, `.claude.json` MCP sunucuları — ve her makine bu öğelerin kendi halini korur.

### Claude Code ile Codex arasında paylaşmak

```bash
codeagent-sync scan                      # what only one tool has, and whether it works in the other
codeagent-sync share skill:foo           # share it
codeagent-sync unshare foo --to claude   # keep it with one tool again
codeagent-sync mark mcp:bar --codex-only # record a decision, change nothing
```

Paylaşılan skill'ler Codex'in okuduğu `~/.agents/skills` altında durur; Claude Code her birine `~/.claude/skills` içindeki bir bağlantıyla ulaşır. `scan` her gerekçenin dosyasını ve satırını gösterir: Claude Code değişkeni ya da aracı kullanan bir skill Claude Code'da kalır; bir MCP sunucusu, mümkünse iki aracın biçimleri arasında dönüştürülür. `--deep`, diğer aracın CLI'ından araçsız ve dosya erişimsiz bir ikinci görüş ister. Kararlar senkronlanan `~/.codeagent-sync/registry.yaml` dosyasında durur; bir makinede cevaplanan soru başka makinede tekrar sorulmaz.

### Bir makineyi diğeri gibi kurmak

```bash
codeagent-sync machines
```

Claude Code, Codex ve MCP sunucularının başlattığı programların her makinede nasıl kurulduğunu — sürümleri ve her birini kuran komutu (native kurucu, npm, Homebrew, pipx, uv, pip) — ve bu makinede neyin eksik olduğunu kurulum komutuyla birlikte gösterir. Senin onayın olmadan hiçbir şey kurulmaz.

---

## Neler Senkronlanır

| Yer | Ne |
|---|---|
| `~/.claude` | `CLAUDE.md`, `settings.json`, `agents/`, `commands/`, `hooks/`, `skills/`, `look-again/`, `statusline.sh`, plugin listeleri |
| `~/.claude.json` | yalnız `mcpServers` |
| `~/.codex` | `AGENTS.md`, `config.toml`, `hooks.json`, `agents/` |
| `~/.agents/skills` | iki aracın da okuduğu skill'ler |
| `~/skills-lock.json` | `npx skills` kilit dosyası |
| `~/.codeagent-sync` | `registry.yaml` (paylaşım kararları), `sync.yaml` (kurallar), `machines/` (envanterler) |

Hiç senkronlanmayanlar: oturumlar, geçmiş, kimlik bilgileri, önbellekler ve bir makineye ait olanlar — güvenilen projeler, onaylanan kancalar, `.claude.json` dosyasının geri kalanı. Komutu bir makinede kurulu olmayan MCP sunucusu, program kurulana kadar o makinede bekletilir.

---

## Sorun Giderme

**Bir şey senkronlanmıyor ya da nedenini bilmiyorsun** — `codeagent-sync doctor` çalıştır: kurulumu, depolamayı, anahtarı, koşullu yazmayı, çakışmaları, kuralları, kancaları ve bu makinede eksik olanları denetler.

**"the credentials may not use the bucket (HTTP 403)"** — API token'ı başka kovalarla sınırlı, ya da kova yok ve token onu oluşturamıyor. Kovayı oluştur ve token'a o kova için okuma-yazma yetkisi ver.

**Codex otomatik senkronlamıyor** — Codex güvenmediği kancaları atlar: Codex'te `/hooks` yazıp codeagent-sync kancalarına güven. `codeagent-sync auto status` durumu gösterir.

**Bir skill bir araçta çalışıyor, diğerinde çalışmıyor** — `codeagent-sync status --check` araçların yok sayacağı skill ve agent'ları listeler; örneğin sembolik bağlantı olan bir `SKILL.md`.

---

## Güvenlik

Her dosya yüklenmeden önce age ile şifrelenir; anahtar parolandan Argon2id ve kovada tutulan rastgele bir tuzla türetilir, yanlış parola hiçbir şeye dokunulmadan anahtar kontrolüyle yakalanır. Depolama dosya adlarını ve boyutlarını görür, içerikleri göremez. Koşullu yazma, aynı anda senkronlayan iki makinenin birbirinin değişikliğini ezmesini önler; silmeler kaybolmaz, kayda geçer.

---

## Teşekkür

[claude-sync](https://github.com/tawanorg/claude-sync) projesinin (MIT) fork'u olarak başladı; bkz. [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

---

## Lisans

MIT — bkz. [LICENSE](LICENSE).

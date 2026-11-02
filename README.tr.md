[![Go Reference](https://pkg.go.dev/badge/github.com/sametbrr/codeagent-sync.svg)](https://pkg.go.dev/github.com/sametbrr/codeagent-sync)
[![GitHub release](https://img.shields.io/github/v/release/sametbrr/codeagent-sync?display_name=tag&sort=semver)](https://github.com/sametbrr/codeagent-sync/releases/latest)
[![npm version](https://badge.fury.io/js/codeagent-sync.svg)](https://badge.fury.io/js/codeagent-sync)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[Hızlı Başlangıç](#hızlı-başlangıç) • [Kurulum](#kurulum) • [Kullanım](#kullanım) • [Sorun Giderme](#sorun-giderme) • [Neler Senkronlanır](#neler-senkronlanır) • [Güvenlik](#güvenlik)

# codeagent-sync

Claude Code ve Codex yapılandırmasını — talimatlar, ayarlar, skill'ler, agent'lar, kancalar, MCP sunucuları ve plugin listeleri — her makinede aynı tutar, uçtan uca şifreli.

> 🇬🇧 For English see [README.md](README.md)

**Claude Code ve Codex için her makinede tek kurulum.** Bir makinede bir ayarı değiştir ya da bir skill veya MCP sunucusu ekle; diğer makineler bir sonraki senkronda alır — üzerine yazılmaz, öğe öğe birleştirilir. Her şey makineden çıkmadan şifrelenir; gereken tek altyapı bir depolama kovası ya da WebDAV sunucusudur.

---

## Hızlı Başlangıç

```bash
npm install -g codeagent-sync   # ya da: pnpm add -g codeagent-sync
codeagent-sync init             # depolama, parola, ilk senkron
```

Diğer her makinede: kurulu bir makinede `codeagent-sync join-code`, yeni makinede `codeagent-sync init --join <kod>`.

---

## Özellikler

| | |
|---|---|
| **İki araç, tek düzen** | `~/.claude`, `~/.claude.json` (yalnız MCP sunucuları), `~/.codex` ve iki aracın da okuduğu `~/.agents/skills` altındaki skill'ler |
| **Uçtan uca şifreli** | Her dosya makineden çıkmadan önce [age](https://age-encryption.org) ile şifrelenir; depolama yalnız dosya adlarını ve boyutlarını görür |
| **Üzerine yazmaz, birleştirir** | Ayar dosyaları öğe öğe birleşir; iki makine farklı ayarları değiştirdiğinde çakışma olmaz, `config.toml` içindeki yorumlar korunur |
| **Taşınabilir yollar** | Ev dizini yolları çevrilir; kullanıcı adı ya da işletim sistemi farklı makineler kendi yollarını alır |
| **Araçlar arası paylaşım** | Bir araçtaki skill ya da MCP sunucusunun diğerinde çalışıp çalışmayacağını değerlendirir, onaylarsan paylaştırır |
| **Otomatik** | Kancalar arka planda senkronlar, çakışmaları ve paylaşılabilecek yenilikleri ajana bir kez bildirir |
| **Neyin senkronlanacağını sen seçersin** | Tüm makineler ya da tek makine için include ve exclude kuralları, tek bir ayara kadar |
| **Makine envanteri** | Araçların ve MCP programlarının diğer makinelerde nasıl kurulduğunu ve bu makinede neyin eksik olduğunu gösterir |
| **Güvenli** | Her değişiklik için yedek ve `undo`, çakışmada üzerine yazmak yerine kenara ayırma, yarışlara karşı koşullu yazma |
| **Depolama** | Cloudflare R2, Amazon S3 ve S3 uyumlu servisler, Google Cloud Storage, WebDAV |

---

## Gereksinimler

- macOS, Linux ya da Windows
- Claude Code ve/veya Codex (bir makinede kurulu olmayan araca orada dokunulmaz)
- Cloudflare R2, S3, GCS ya da bir WebDAV sunucusunda bir kova ve ona okuma-yazma yetkisi olan kimlik bilgileri
- npm ya da pnpm ile kurmak için Node.js 22 ya da üzeri (LTS); ya da hazır ikili dosya, veya kaynaktan derlemek için Go 1.24 ya da üzeri

---

## Kurulum

```bash
npm install -g codeagent-sync
pnpm add -g codeagent-sync
```

Paket, sistemine uygun programı sürümün checksum'larıyla doğrulayıp `~/.local/bin/codeagent-sync` konumuna kurar (Windows'ta `%USERPROFILE%\.local\bin`). pnpm kurulum betiklerini varsayılan olarak çalıştırmaz; o durumda program ilk çalıştırmada kurulur. Her makinede aynı yerde tut: otomatik senkronun kancaları programı oradan başlatır. `codeagent-sync update` onu en son sürümle değiştirir.

Paket yöneticisi kendi başlatıcısını aynı yola bağlıyorsa (npm'in global dizini `~/.local`, pnpm'in `PNPM_HOME` değeri `~/.local/bin` ise) program `~/.codeagent-sync/bin` altına kurulur; `~/.local/bin/codeagent-sync` başlatıcıda kalır ve kancalar başlatıcıyı çalıştırır. npm orada zaten duran bir programın üzerine yazmayı reddeder; böyle bir makinede npm ile kurmadan önce programı bir kez kenara al: `mkdir -p ~/.codeagent-sync/bin && mv ~/.local/bin/codeagent-sync ~/.codeagent-sync/bin/`.

<details>
<summary><strong>Hazır ikili dosyalar ve kaynaktan derleme</strong></summary>

macOS, Linux ve Windows için ikili dosyalar [releases](https://github.com/sametbrr/codeagent-sync/releases) sayfasında: sistemine uygun olanın adını `codeagent-sync` yapıp `~/.local/bin` içine koy.

Kaynaktan derlemek için (Go 1.24 ya da üzeri):

```bash
git clone https://github.com/sametbrr/codeagent-sync && cd codeagent-sync
make install
```

</details>

### Kaldırma

```bash
codeagent-sync auto disable       # kancaları kaldır; diğer makineler bir sonraki senkronda aynısını yapar
npm uninstall -g codeagent-sync   # ya da: pnpm remove -g codeagent-sync
rm -f ~/.local/bin/codeagent-sync
rm -rf ~/.codeagent-sync          # yerel kurallar, kararlar ve kurulum
```

Claude Code ve Codex dosyaların olduğu gibi kalır. Şifreli kopyalar, sen oradan silene kadar kovada durur.

---

## Kullanım

```bash
# Kurulum
codeagent-sync init                       # depolama, parola, ilk senkron
codeagent-sync join-code                  # başka bir makineyi kuracak kodu yazdır
codeagent-sync init --join <code>         # yeni makineyi bu kodla kur
codeagent-sync auto enable                # kancalarla otomatik senkron

# Günlük kullanım
codeagent-sync sync                       # diğerlerinin değişikliklerini indir, bu makinedekileri yükle
codeagent-sync pull                       # yalnız indir
codeagent-sync push                       # yalnız yükle
codeagent-sync status                     # hiçbir şeyi değiştirmeden senkronun ne yapacağını göster
codeagent-sync conflicts                  # çakışma yüzünden kenara ayrılan sürümler
codeagent-sync undo                       # bir değişikliği geri al
codeagent-sync paths                      # neler senkronlanıyor, kurallar neler
codeagent-sync scan                       # yalnız bir araçta olanlar
codeagent-sync share <name>               # diğer araçla paylaş
codeagent-sync unshare <name> --to claude # yeniden tek araçta tut
codeagent-sync mark <name> --codex-only   # kararı kaydet, hiçbir şeyi değiştirme
codeagent-sync machines                   # her makine nasıl kurulu, bu makinede ne eksik

# Bakım
codeagent-sync doctor                     # kurulumu, depolamayı, anahtarı ve kancaları denetle
codeagent-sync update                     # programı en son sürümle değiştir
codeagent-sync auto disable               # kancaları kaldır
```

### İlk makineyi kurmak

```bash
codeagent-sync init
```

`init` depolamayı ve bir parola sorar, kimlik bilgileri izin veriyorsa kovayı oluşturur, neyi yükleyeceğini listeleyip onay ister, sonra otomatik senkronu önerir. Parola kurtarılamaz: parola yöneticine kaydet. Parola yerine bir age anahtar dosyası da kullanabilirsin (`--key-file`).

### Başka bir makine eklemek

```bash
codeagent-sync join-code                 # kurulu bir makinede
codeagent-sync init --join cas1-…        # yeni makinede, aynı parolayla
```

Kod, depolama ayarlarını kovanın parolasıyla şifreli taşır. Bir makinenin ilk senkronunda yalnız o makinede olan dosyalar senin onayınla yüklenir; diğer makinelerden gelen dosyalar yedeklenerek yazılır.

### Günlük kullanım

```bash
codeagent-sync sync        # diğerlerinin değişikliklerini indir, bu makinedekileri yükle
codeagent-sync status      # hiçbir şeyi değiştirmeden senkronun ne yapacağını göster
codeagent-sync conflicts   # kenara ayrılan sürümler; çözmek için: conflicts resolve <path> --keep local|remote
codeagent-sync undo        # bir değişikliği geri al; undo --list hepsini gösterir
```

`pull` ve `push` yalnız tek yönde senkronlar. `sync`, `status`, `scan`, `conflicts`, `doctor`, `paths`, `machines` ve `auto status` `--json` ile JSON çıktı verir.

### Otomatik senkron

```bash
codeagent-sync auto enable
```

Claude Code ve Codex'e kancalar ekler: oturum başladığında ve her yanıttan sonra arka planda senkronlar; bilmen gerekenleri — bir çakışma, paylaşılabilecek yeni bir şey, başka makinede olan bir program — bir sonraki mesajında ajana bir kez iletir. Kancalar senkronlanan ayarların parçasıdır, diğer makinelerine de ulaşır. Codex yeni kancaları ancak onlara güvendiğinde çalıştırır: her makinede bir kez Codex'te `/hooks` yaz.

### Neyin senkronlanacağını seçmek

```bash
codeagent-sync paths                                    # neler senkronlanıyor, kurallar neler
codeagent-sync paths include claude/plans/**            # daha fazlasını senkronla
codeagent-sync paths exclude claude/look-again/**       # daha azını senkronla
codeagent-sync paths exclude claude-state/.claude.json  # Claude Code'un MCP sunucuları hariç
codeagent-sync paths exclude claude/settings.json#permissions --local
codeagent-sync paths reset claude/plans/**              # bir kuralı kaldır
```

Kurallar `<kök>/<kalıp>` biçimindedir (kökler: `claude`, `codex`, `agents`, `claude-state`, `home`, `codeagent`). Senkronlanan `~/.codeagent-sync/sync.yaml` dosyasında durur, böylece her makine aynı kurala uyar; `--local` ile yalnız o makine için `sync.local.yaml` dosyasına yazılır. Dışarı alınan yol senkronlanmayı bırakır ama hiçbir yerde silinmez. `#` işaretinden sonra bir exclude, bir ayar dosyasının öğelerini adlandırır — `settings.json` anahtarları, `mcp_servers.*` gibi `config.toml` tabloları, `.claude.json` MCP sunucuları — ve her makine bu öğelerin kendi halini korur.

### Claude Code ile Codex arasında paylaşmak

```bash
codeagent-sync scan                      # yalnız bir araçta olanlar ve diğerinde çalışıp çalışmadığı
codeagent-sync share skill:foo           # paylaş
codeagent-sync unshare foo --to claude   # yeniden tek araçta tut
codeagent-sync mark mcp:bar --codex-only # kararı kaydet, hiçbir şeyi değiştirme
```

Paylaşılan skill'ler Codex'in okuduğu `~/.agents/skills` altında durur; Claude Code her birine `~/.claude/skills` içindeki bir bağlantıyla ulaşır. `scan` her gerekçenin dosyasını ve satırını gösterir: Claude Code değişkeni ya da aracı kullanan bir skill Claude Code'da kalır; bir MCP sunucusu, mümkünse iki aracın biçimleri arasında dönüştürülür. `--deep`, diğer aracın CLI'ından araçsız ve dosya erişimsiz bir ikinci görüş ister. Kararlar senkronlanan `~/.codeagent-sync/registry.yaml` dosyasında durur; bir makinede cevaplanan soru başka makinede tekrar sorulmaz.

Bir projenin kendi MCP sunucuları da karşılaştırılır: `.mcp.json` içindekiler (Claude Code) ile `.codex/config.toml` içindekiler (Codex; bu dosyayı yalnız güvendiği projelerde okur). Paylaşmak, sunucuyu aynı projenin diğer dosyasına yazar ve karar o proje için kaydedilir; sunucu adı tek başına belirsizse `mcp:<proje kökü>/<sunucu>` biçiminde verilir.

### Bir makineyi diğeri gibi kurmak

```bash
codeagent-sync machines
```

Claude Code, Codex ve MCP sunucularının başlattığı programların her makinede nasıl kurulduğunu — sürümleri ve her birini kuran komutu (native kurucu, npm, Homebrew, pipx, uv, pip) — ve bu makinede neyin eksik olduğunu kurulum komutuyla birlikte gösterir. Senin onayın olmadan hiçbir şey kurulmaz.

---

## Sorun Giderme

**Bir şey senkronlanmıyor ya da nedenini bilmiyorsun** — `codeagent-sync doctor` çalıştır: kurulumu, depolamayı, anahtarı, koşullu yazmayı, çakışmaları, kuralları, kancaları ve bu makinede eksik olanları denetler.

**"the credentials may not use the bucket (HTTP 403)"** — API token'ı başka kovalarla sınırlı, ya da kova yok ve token onu oluşturamıyor. Kovayı oluştur ve token'a o kova için okuma-yazma yetkisi ver.

**Codex otomatik senkronlamıyor** — Codex güvenmediği kancaları atlar: Codex'te `/hooks` yazıp codeagent-sync kancalarına güven. `codeagent-sync auto status` durumu gösterir.

**Bir skill bir araçta çalışıyor, diğerinde çalışmıyor** — `codeagent-sync status --check` araçların yok sayacağı skill ve agent'ları listeler; örneğin sembolik bağlantı olan bir `SKILL.md`.

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
| her proje | `.mcp.json`, `CLAUDE.local.md`, `.claude/settings.json`, `.claude/settings.local.json`, `.claude/agents/`, `.claude/commands/`, `.claude/skills/`, `.codex/config.toml`, `.codex/agents/` — git'in takip ettiği dosyalar hariç |

Projeler, Claude Code ile Codex'in proje olarak listelediği dizinlerdir. Yollarıyla değil git remote'larıyla tanınırlar; bu yüzden bir projenin ayarları, başka makinede nerede klonlanmış olursa olsun aynı projeye ulaşır. Bir repo `origin` adresiyle tanınır (https ve ssh biçimleri aynı sayılır), birkaç repo barındıran düz bir dizin ise o repoların remote'larıyla. Worktree'ler, remote'u olmayan repolar ve aynı projeye denk gelen iki dizin senkronlanmaz; bulunan projeleri `codeagent-sync paths` listeler.

Hiç senkronlanmayanlar: oturumlar, geçmiş, kimlik bilgileri, önbellekler ve bir makineye ait olanlar — güvenilen projeler, onaylanan kancalar, `.claude.json` dosyasının geri kalanı. Komutu bir makinede kurulu olmayan MCP sunucusu, program kurulana kadar o makinede bekletilir.

---

## Güvenlik

Her dosya yüklenmeden önce age ile şifrelenir; anahtar parolandan Argon2id ve kovada tutulan rastgele bir tuzla türetilir, yanlış parola hiçbir şeye dokunulmadan anahtar kontrolüyle yakalanır. Depolama dosya adlarını ve boyutlarını görür, içerikleri göremez. Koşullu yazma, aynı anda senkronlayan iki makinenin birbirinin değişikliğini ezmesini önler; silmeler kaybolmaz, kayda geçer.

---

## Teşekkür

[claude-sync](https://github.com/tawanorg/claude-sync) projesinin (MIT) fork'u olarak başladı; bkz. [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

---

## Katkıda Bulunma

- Sorular ve fikirler: [Discussions](https://github.com/sametbrr/codeagent-sync/discussions)
- Hatalar: [issue takibi](https://github.com/sametbrr/codeagent-sync/issues)
- Pull request'ler: önce [katkı rehberini](https://github.com/sametbrr/.github/blob/main/CONTRIBUTING.md) okuyun
- Güvenlik bildirimleri: herkese açık issue yerine [güvenlik politikasını](https://github.com/sametbrr/codeagent-sync/security/policy) izleyin

---

## Lisans

MIT — bkz. [LICENSE](LICENSE).

---

<div align="center">

[Hata Bildir](https://github.com/sametbrr/codeagent-sync/issues) · [Özellik İste](https://github.com/sametbrr/codeagent-sync/issues)

</div>

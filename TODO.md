# plug — TODO / plan de travail

_État : juillet 2026._

**Contexte.** Le **service SYSTEM Windows** (sans-admin + multicluster) est **validé de
bout en bout** sur une vraie machine : install en Git Bash (admin une fois) → service ;
puis `plug` **sans admin** partout (token non-élevé prouvé), multicluster concurrent
inclus. Cold-start ramené de ~15 s à **~0,8 s**. Reste surtout l'**e2e Windows en CI**,
quelques tests unitaires et des dettes. Archi : `docs/windows-service.md`.

---

## 🟡 Verrouiller Windows en CI — le principal reste
e2e **« tout Windows-container »** sur runner hébergé (`windows-latest`, Windows
containers, **sans WSL2**) — fige **install + service + résolution par nom (getaddrinfo)** :
- [ ] Image agent `servercore` + OpenSSH Server (`Dockerfile.windows`, aligné `ltsc2022`)
- [ ] Petite image HTTP WinCtr (nginx/servercore) comme service cible
- [ ] Workflow `windows-latest` : install (Git Bash) → `install-service` → `plug curl` → assert **200**
- [ ] Prouver le **sans-admin** avec un token restreint (comme le test manuel `schtasks /rl LIMITED`)

Complément CI hébergé, sans Docker :
- [ ] Étendre `plug selftest` Windows : résolution **par nom via `getaddrinfo`** + service
      _(le selftest actuel court-circuite getaddrinfo → il n'aurait pas vu le bug mono-label)_

## 🟢 e2e complet Windows (la grille) — sur self-hosted
- [ ] Runner **self-hosted** = PC Windows + Docker Desktop/WSL2 (nested-virt dispo)
- [ ] Réutiliser l'**agent Linux existant** + les services Linux (pas de WinCtr à maintenir)
- [ ] Grille langages × protocoles **native Windows** : Go/Node/Python/Java × httpbin/pg/redis/mongo/amqp/mqtt/grpc/ws

## 🔵 Tests unitaires des découvertes + dédup
- [ ] `answerDNS` : strip `.plug` → mint le nom nu (fix DNS mono-label)
- [ ] `ensureVersion` : suffixe `.exe` sur Windows · `ensureWintunBeside` : copie de `wintun.dll`
- [ ] `DialContext` : un rejet de canal (`*ssh.OpenChannelError`) ne reconnecte pas (fix multi-session)
- [ ] `setSystemNRPT`/`clearSystemNRPT` : round-trip registre `DnsPolicyConfig`
- [x] `walkToCluster` : recyclage PID (`TestWalkToClusterRecycledPID`)
- [ ] **Factoriser** `registry_windows`/`graft_windows` avec les `_darwin` (dédup) — dupliqués
      volontairement pour ne pas risquer le mac validé ; à unifier maintenant que Windows est validé

## ⚪ Dettes / plus tard
- [ ] Version service vs launcher : rafraîchir le binaire du service au bump (ou auto)
- [ ] Retirer les directives compose obsolètes (`PLUG_HOOK_DEBUG`, `seccomp:unconfined`, `SYS_PTRACE`)
- [ ] IPv6 : fake-pool + tunneling des littéraux v6 (roadmap)
- [ ] Généraliser le selftest multi-protocole par OS (roadmap)
- [ ] **macOS multicluster** : mécanisme PID-at-connect partagé et prouvé sur Windows → le valider aussi sur mac

---

## ✅ Acquis (juillet 2026)
- [x] **Service SYSTEM Windows validé de bout en bout** : install Git Bash (admin) → service ; `plug` sans admin (token non-élevé), multicluster concurrent
- [x] **Installeur Git Bash pur** (`install.sh` ; `PLUG_HOST` ; `-n` contre le hang ssh) ; l'agent **sert `wintun.dll`** (plus de dépendance wintun.net)
- [x] **Cold-start ~15 s → ~0,8 s** (NRPT en registre, tunnel ouvert en ~0,3 s, grâce de 20 s par cluster)
- [x] **Multi-session concurrente** corrigée — un rejet de canal ne reconnecte plus (cross-OS)
- [x] **version/download via `crypto/ssh`** sur Windows (fini le gel du binaire ssh sur pipe)
- [x] **Host-key reset accessible** : known_hosts dans `%ProgramData%\plug` (user-writable) + message clair
- [x] Datapath Windows validé en réel (WinTUN + netstack + DNS `.plug`/NRPT + splice par nom)
- [x] Durcissement attribution PID contre le recyclage (mac/linux/windows)
- [x] README + coverage matrix à jour (Windows validé de bout en bout)
- [x] Multicluster macOS validé en réel ; multicluster Linux (mount namespaces)

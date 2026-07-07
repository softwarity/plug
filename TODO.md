# plug — TODO / plan de travail

_État : juillet 2026._

**Contexte.** Le datapath **Windows** est validé en usage réel (mono-cluster, résolution
par nom : `plug curl http://user-mng-frontend:8081/…` → 200). Le **service SYSTEM**
(sans-admin + multicluster) est écrit et **compile-validé** sur les 3 OS (mac intact),
mais **pas encore validé en runtime**. Archi + plan de test détaillés : `docs/windows-service.md`.

---

## 🔴 Immédiat — valider le service Windows en runtime
- [ ] Rebuild image depuis `HEAD`, update agent, réinstaller (le service s'installe, **1 UAC**)
- [ ] `Get-Service plug` → `Stopped` (on-demand) ; `sc.exe qc plug` → binPath `… __plug-daemon`, START_TYPE DEMAND
- [ ] **Terminal NON-élevé** : `plug curl http://<svc>:<port>/…` → le service démarre, ça résout, **200**
- [ ] Multicluster : `plug -p a` ‖ `plug -p b` (2 agents) → chacun n'atteint que son cluster
- [ ] Teardown : service s'arrête ~30 s après le dernier client ; `plug down` l'arrête
- [ ] Points de risque à vérifier (cf. `docs/windows-service.md`) :
  - [ ] ACL du service (SDDL) : un user **non-admin** peut-il START/STOP ?
  - [ ] Service en **session 0** : WinTUN + NRPT s'appliquent-ils machine-wide ?
  - [ ] Ordre d'arrêt `Execute` (StopPending→Stopped) : pas de service "hung"
  - [ ] Version **service vs launcher** (le service pointe un `plug.exe` figé)

## 🟠 PC Windows de test — pour valider en autonomie (fini le ping-pong)
- [ ] **OpenSSH _Server_** activé + PC joignable sur le réseau local  ← le point clé
- [ ] Compte **admin** pour la session SSH (token élevé, pas de UAC interactif)
- [ ] **Go** (1.23+) + **Git for Windows**
- [ ] **Docker Desktop** (WSL2) — pour agent + services en local
- [ ] Client OpenSSH (souvent déjà présent)

## 🟡 Verrouiller les 3 points fragiles en CI — une fois le service validé
e2e **« tout Windows-container »** sur runner hébergé (`windows-latest`, Windows
containers, **sans WSL2**) — fige **install + service + résolution par nom (getaddrinfo)** :
- [ ] Réécrire `serve-binary` en **PowerShell** (le `sh` ne tourne pas sous Windows)
- [ ] Image agent `servercore` + OpenSSH Server (`Dockerfile.windows`, aligné `ltsc2022`)
- [ ] Petite image HTTP WinCtr (nginx/servercore) comme service cible
- [ ] Workflow `windows-latest` : install → `install-service` → `plug curl` → assert **200**
- [ ] (partiel) prouver le **sans-admin** avec un token restreint / user standard

Complément CI hébergé, sans Docker :
- [ ] Étendre `plug selftest` Windows : résolution **par nom via `getaddrinfo`** + service
      _(le selftest actuel court-circuite getaddrinfo → il n'aurait pas vu le bug mono-label)_

## 🟢 e2e complet Windows (la grille) — sur self-hosted
- [ ] Runner **self-hosted** = PC Windows + Docker Desktop/WSL2 (nested-virt dispo)
- [ ] Réutiliser l'**agent Linux existant** + les services Linux (pas de WinCtr à maintenir)
- [ ] Grille langages × protocoles **native Windows** : Go/Node/Python/Java × httpbin/pg/redis/mongo/amqp/mqtt/grpc/ws

## 🔵 Audit + tests des découvertes du jour
- [ ] Audit `v1.2.0..HEAD` : code mort, commentaires périmés, approches contradictoires
      _(déjà corrigé : le commentaire « NRPT catch-all » obsolète)_
- [ ] Tests unitaires des découvertes :
  - [ ] `answerDNS` : strip `.plug` → mint le nom nu (fix DNS mono-label)
  - [ ] `ensureVersion` : suffixe `.exe` sur Windows
  - [ ] `ensureWintunBeside` : copie de `wintun.dll`
  - [x] `walkToCluster` : recyclage PID (fait — `TestWalkToClusterRecycledPID`)
- [ ] **Factoriser** `registry_windows`/`graft_windows` avec les `_darwin` (dédup) — dupliqués
      volontairement pour ne pas risquer le mac validé ; à unifier une fois Windows validé

## ⚪ Dettes / plus tard
- [ ] README section Windows : annoncer le **sans-admin** _une fois le service validé_ (pas avant)
- [ ] Version service vs launcher : rafraîchir le binaire du service au bump (ou auto)
- [ ] Retirer les directives compose obsolètes (`PLUG_HOOK_DEBUG`, `seccomp:unconfined`, `SYS_PTRACE`)
- [ ] IPv6 : fake-pool + tunneling des littéraux v6 (roadmap)
- [ ] Généraliser le selftest multi-protocole par OS (roadmap)

---

## ✅ Acquis (juillet 2026)
- [x] Datapath Windows validé en réel (WinTUN + netstack + DNS `.plug`/NRPT + splice par nom)
- [x] Durcissement attribution PID contre le recyclage (mac/linux/windows)
- [x] Installeur Windows robuste (Git Bash, `PLUG_HOST`, known_hosts jetable, `.exe`, `wintun.dll`)
- [x] Service SCM Windows + `coreRun` à deux voies (service / fallback in-process) — **compile-validé**
- [x] Multicluster macOS validé en réel ; multicluster Linux (mount namespaces)

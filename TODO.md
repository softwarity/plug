# plug — TODO / plan de travail

_État : 17 juillet 2026 — post-2.0.0, takeover implémenté (→ 2.1.0)._

**Contexte.** La **2.0.0** est publiée. Elle apporte le **sens retour** : `plug -s
name:cluster-port:local-port` publie ton process dans le cluster sous un nom
(remote-forward sshd, connexion SSH dédiée à la session), le nom est
**provisionné à la volée** (signpost Docker via le socket, service Swarm sur
overlay non-attachable, Service k8s via un rôle RBAC Services-only), et **`-s`
est devenu obligatoire** — une seule forme d'invocation. La **détection de
collision** refuse un nom déjà pris (message précis par backend). Le transport
**s'auto-répare** : keepalive borné qui tue une connexion zombie (sleep / VPN /
proxy Docker Desktop) puis reconnecte et **re-provisionne** le nom. Licence
passée en **AGPL-3.0**. Windows est désormais une **jambe e2e complète en CI**
(mesh Tailscale, natif, sans WSL2) — tout l'ancien chantier « verrouiller
Windows » est bouclé.

CI par push, 3 OS : install-depuis-cluster → grille 4 langages × 8 protocoles →
multicluster simultané → outage recovery → env passthrough → `-s` → gateway
callback → collision → compat launcher/core.

---

## 🟡 Combler les trous « banc → CI » (le principal reste)
Comportements **prouvés au runtime en local**, pas encore rejoués en CI :
- [ ] **Takeover Swarm en CI** (banc M5 ✅ : scale-0 → trafic local → scale-back au replica count) ; **takeover k8s** : codé, jamais exécuté au runtime → banc k8s.
- [ ] **Takeover : boot-gc + re-park au reconnect en CI** (banc M5 ✅ : agent kill → gc restaure → rearm re-parque → restore final).
- [ ] **Self-heal en CI** : le keepalive tue le zombie puis reconnecte + re-provisionne — banc seulement ; Windows non prouvé. Cellule e2e : couper le chemin (sleep / agent restart), asserter la reprise.
- [ ] **Re-arm `-s` après reconnexion** en CI (aujourd'hui banc local).
- [ ] **Backend k8s Service pour `-s`** : codé, pas encore testé au runtime (RBAC Services-only) → cellule e2e k8s.
- [ ] **Swarm en CI** : agent sur overlay non-attachable — banc seulement (seul Compose est en CI).
- [ ] **Kubernetes NodePort / `kubectl port-forward`** : banc seulement.
- [ ] **Windows sous VPN d'entreprise** : non prouvé (macOS OK avec GlobalProtect).
- [ ] **Sessions longues & charge** : heures, gros transferts, nombreuses connexions, sleep/wake — non exercés.

## 🟣 UDP par nom (relais de datagrammes) — 2.1.0
Le tunnel ne porte que du TCP (SSH `direct-tcpip` = stream-only). Le client capte
**déjà** l'UDP (protocole enregistré, `gonet.NewUDPConn` utilisé pour le DNS) et
le **droppe** hors DNS (`cli/internal/tun/netstack.go:170-171`). Plan :
- [ ] **Drop-loud d'abord** (petit fix, tout de suite) : logguer (rate-limité) « udp `<name>:<port>` non tunnelé » au lieu de jeter en silence — fin du hang sans diagnostic (flaggé MAJEUR dans `audit.md`).
- [ ] **Client** : remplacer le drop par un forwarder UDP — `tab.lookup` → nom, `df(srcPort)` → cluster (réutilise l'attribution TCP), ouvrir un canal vers l'agent, **framing longueur-préfixée** des datagrammes.
- [ ] **Agent** : sous-commande `plug-agent udp-relay <name> <port>` (invoquée en session SSH comme `serve-name`) → résout via le resolver cluster, `net.DialUDP`, relaie les datagrammes framés dans les deux sens.
- [ ] **Cycle de vie** : flux synthétique par `(srcport, dst)` + **idle-timeout** pour reaper canal + relais (UDP sans-connexion).
- [ ] **Négo de version** : vieil agent → « udp-relay not found » → dégrader proprement en drop-loud (comme serve-name).
- [ ] **e2e ×3 OS** : DNS-sur-UDP vers un CoreDNS + un echo UDP (type StatsD) ; MàJ coverage (ligne UDP `✕` → `!`/`✓`) + roadmap.

Limite assumée : datagrammes → stream fiable/ordonné (HOL blocking, latence). OK
pour DNS / StatsD / syslog / requête-réponse ; pas pour média temps-réel. QUIC =
UDP → porté mais QUIC-over-TCP est pathologique (les clients retombent en TCP de
toute façon).

## 🔵 Transport & intégration (roadmap)
- [ ] **IPv6** : fake-pool v6 + tunneling des littéraux v6 (fakes IPv4 aujourd'hui ; service par nom déjà OK).
- [ ] **Transport `kubectl exec`** : tunnel via `kubectl exec` sur un pod nu — zéro port exposé, accès gouverné par le kubeconfig RBAC (adoucit le compromis no-auth).
- [ ] **Gateway hôte du tunnel** : la gateway (Java) déjà déployée héberge l'endpoint et l'active dynamiquement — son auth devant. Fin de l'agent dédié.

## 🔴 macOS : re-assert DNS trop agressif (churn mDNSResponder) — 2.1.x
Diagnostiqué en live (17/07) : `locationd` fait scanner le Wi-Fi en continu →
ré-évaluation auto-join → **DHCP re-publish ~2/min** → configd re-dérive
`State:/Network/Service/<en0>/DNS` (écrase l'override) → le watchdog plug
(`route_darwin.go:159-191`, tick 3 s) ré-écrit **+ `flushcache` + `HUP
mDNSResponder`** à chaque fois (~4 400 re-asserts/24 h en rafales) → la pile de
résolution redémarre sans arrêt → **échecs getaddrinfo intermittents**
(`Could not resolve host: github.com`…) pour TOUTE la machine.
- [ ] **Re-assert silencieux** : si `Global`+`Setup` pointent encore `dnsIP` (ce que mDNSResponder consomme), ré-écrire la clé Service SANS `flushDNS()` — le flush+HUP n'est justifié que si la config effective a réellement divergé.
- [ ] **Débounce** : max 1 flush/HUP par 30 s, même en rafale d'événements configd.
- [ ] **Timestamper** les lignes du daemon.log (le diagnostic a buté sur des re-asserts non datés).

## ⚪ Dettes / plus tard
- [ ] **Tests unitaires** (comportements déjà prouvés en e2e, faible priorité) : `answerDNS` strip `.plug` → mint du nom nu ; round-trip registre NRPT (`setSystemNRPT` / `clearSystemNRPT`) ; `DialContext` — un rejet de canal (`*ssh.OpenChannelError`) ne reconnecte pas ; `ensureVersion` (`.exe`) / `ensureWintunBeside`.
- [ ] **Factoriser** `registry_windows` / `graft_windows` avec les `_darwin` (dupliqués volontairement le temps de valider Windows — à unifier maintenant).
- [ ] **Version service vs launcher** : rafraîchir le binaire du service au bump (ou auto).
- [ ] Retirer les directives compose obsolètes (`PLUG_HOOK_DEBUG`, `seccomp:unconfined`, `SYS_PTRACE`) si encore présentes.
- [ ] Nettoyage post-2.0.0 : tag Docker Hub `plug-bidi` (branche de dev) à supprimer.

---

## ✅ Acquis

### Post-2.0.0 → 2.1.0 (17 juillet 2026)
- [x] **Takeover par défaut** : un nom `-s` tenu par le service déployé est **parqué** pour la session et **restauré** à la fin — conteneurs stoppés (Compose, **e2e CI**), service Swarm scalé 0 → replica count d'origine (**banc M5**), Service k8s re-pointé via annotation-reçu (codé). Reçu de parking sur le signpost → restore par unserve / **boot-gc** (crash agent) / **re-park au reconnect** (banc M5 ✅). Signpost créé AVANT le park (pas de trou DNS — fuite upstream prouvée au banc). D'abord opt-in `--takeover`, puis **défaut** (lancer `plug -s` est déjà l'intention ; flag accepté en no-op) ; un nom tenu par une **autre session** reste refusé ; vieil agent 2.0.x → fallback auto sur son comportement (refus + hint upgrade) ; RBAC k8s +update/patch ; cellules e2e `takeover` + `collision` (inter-sessions) ×3 OS, noms/ports par jambe.

### 2.0.0 (juillet 2026)
- [x] **Sens retour `-s`** : remote-forward sshd, connexion SSH dédiée à la session, port fermé avec la session — e2e ×3 OS
- [x] **Provisionnement dynamique du nom** : signpost Docker (socket), service Swarm sur overlay non-attachable, Service k8s (RBAC Services-only) ; fallback alias statique
- [x] **Gateway callback** : appelant externe → gateway publiée → nom `-s` → sink local du runner (id + chemin complet round-trip) — le cas API-gateway, prouvé depuis l'extérieur
- [x] **`-s` obligatoire** (breaking) : une seule forme d'invocation ; validation du nom côté client (label RFC 1035)
- [x] **Détection de collision** : nom déjà pris refusé, message précis par backend (`docker rm -f` / `docker service rm` / `kubectl delete`) ; scoping réseau du check Swarm ; fix alias-null (`/containers/json` n'a pas les alias → inspect)
- [x] **Self-heal keepalive** : `pingOK` borné, `dropDead` du zombie, `PLUG_KEEPALIVE_SECS` ; re-provision sur reconnexion (`OnRearm`)
- [x] **Version floor** : refus de `-s` contre un agent < 2.0.0 (`coreMajor`)
- [x] **AGPL-3.0** : `LICENSE` racine, badges, section README + contact commercial, `THIRD_PARTY_LICENSES.md`
- [x] **Doc** : About (schéma bidirectionnel + comparatif objectif mirrord/Telepresence), how-it-works bidirectionnel, Getting started allégé
- [x] **CI e2e par famille** (pass/fail visible par étape) ; compat launcher passe `-s`

### Antérieur (1.x)
- [x] **Windows** : service SYSTEM sans-admin + multicluster (PID-at-connect) ; **jambe e2e complète en CI** (mesh, natif, sans WSL2) ; cold-start ~15 s → ~0,8 s
- [x] **Multicluster prouvé en CI sur les 3 OS** (mount-ns Linux · daemon macOS · service Windows), macOS simultané inclus
- [x] Datapath userspace-TUN (tout runtime, Go & gRPC), split-horizon, DNS in-stack sous VPN
- [x] Install depuis le cluster + launcher (versions par cluster) + privilège une seule fois (setcap / setuid / service)
- [x] Grille e2e 8 protocoles × 4 langages, native sur les 3 OS

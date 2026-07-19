# plug — TODO / plan de travail

_État : 18 juillet 2026 — **2.1.0 publiée** (takeover par défaut, 3 familles de
clusters en CI, fix DNS macOS)._

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

CI par push, 3 OS × **3 familles de clusters** (Compose, Swarm mono-nœud,
Kubernetes/kind — 9 jambes, 6 clusters) : install-depuis-cluster → grille 4
langages × 8 protocoles → multicluster simultané → outage recovery → env
passthrough → `-s` → gateway callback → collision → takeover → compat
launcher/core (famille Compose). L'image ne publie que si les 9 jambes sont
vertes.

---

## 🟡 Ce qui reste hors CI (le banc → CI est bouclé)
- [ ] **Windows sous VPN d'entreprise** : non prouvé (macOS OK avec GlobalProtect). Il faut un poste Windows avec un vrai client VPN corpo — la box 192.168.2.17 ferait l'affaire si on y installe le client ; banc ~30 min ensuite.
- [ ] **Sessions longues & charge** : heures, gros transferts, sleep/wake. Piste actée : un workflow « soak » cron hebdo (session tenue 5-6 h, transferts gros volumes, asserts RSS/reconnexions) ; le sleep/wake réel reste un banc local assisté.

## 🟣 UDP par nom (relais de datagrammes) — REPORTÉ (décision 18/07)
La motivation « HTTP/3 » ne tient pas : le relais passerait par le tunnel TCP →
QUIC-over-TCP est pathologique (HOL blocking, tout l'intérêt de QUIC perdu) et
les clients h3 retombent en h2 proprement ; en intra-cluster personne ne parle
h3. Le drop-loud (livré) rend le manque visible et diagnostiqué. À rouvrir si
un cas DNS-vers-CoreDNS / StatsD / syslog réel mord. Plan conservé :
Le tunnel ne porte que du TCP (SSH `direct-tcpip` = stream-only). Le client capte
**déjà** l'UDP (protocole enregistré, `gonet.NewUDPConn` utilisé pour le DNS) et
le droppait en silence hors DNS. Plan :
- [x] **Drop-loud** (18/07) : un flux UDP vers un nom minté loggue « udp `<name>:<port>` dropped — plug tunnels TCP only » (rate-limité 30 s, `udpDropLimiter`) — fin du hang sans diagnostic (flaggé MAJEUR dans `audit.md`).
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

## 🟠 Fuite DNS Docker-Desktop-sur-poste-plugué — RÉSOLUE (18/07)
Docker forwarde les noms inconnus du cluster vers le resolver de la VM → hérite
du DNS du Mac → **resolver plug** quand des sessions tournent → un nom ABSENT du
cluster résolvait vers une fake IP `198.18.x.x` (connection refused au lieu
d'unknown host).
- [x] **Doc** : callout page Swarm — remède `daemon.json "dns": ["1.1.1.1"]` (devenu défense-en-profondeur).
- [x] **Mitigation produit** (18/07) : le **mint est désormais vérifié** — avant de minter un nom nu, le CLI demande à l'agent (verbe `resolve`, cache 5 min/30 s nég) si le nom existe dans un cluster connecté ; absent partout → **NXDOMAIN honnête**. Le helper **filtre les échos 198.18/15** (une réponse dans la plage plug = la boucle poste-plugué, jamais un vrai service) — le fix est donc immunisé contre la boucle qu'il corrige. Vieil agent sans le verbe → mint comme avant (contrat de dégradation). Banc compose ✅ (`no such host` immédiat au lieu de 4 timeouts + refused) ; cellule e2e « dns honesty » ×9 jambes.

## ⚪ Dettes / plus tard
- [x] **Tests unitaires** (18/07) : `answerDNS` strip `.plug` → mint du nom nu (même fake, mapping vers le nom nu) ; round-trip NRPT (`route_windows_test.go`, skip sans élévation — tourne sur la jambe test Windows) ; `DialContext` — un `*ssh.OpenChannelError` ne reconnecte PAS (serveur SSH mock in-process qui rejette les canaux et compte les connexions) ; `ensureVersion` cache-hit + rejet d'un cache tronqué (`launcher_test.go`, HOME isolé).
- [x] **Factoriser `registry_windows`/`registry_darwin`** (18/07) : le commun (95 % identique) vit dans `registry.go` (`darwin || windows`) avec `ClusterHash` ; par-OS il ne reste que `processAlive`. Les tests registry tournent désormais AUSSI sur Windows. `graft_*` : PAS factorisé — divergence structurelle assumée (flock/leader-election macOS vs service unique Windows), plus rien d'accidentellement dupliqué.
- [ ] **Version service vs launcher** : rafraîchir le binaire du service au bump (ou auto) — chantier de design (le service est SYSTEM/root : qui déclenche, avec quels droits), pas un quick-win.
- [x] Retirer les directives compose obsolètes (18/07) : `PLUG_HOOK_DEBUG`, `seccomp:unconfined`, `SYS_PTRACE` retirées des 4 clients e2e (aucun usage dans le code ; `apparmor:unconfined` reste — le bind mount-ns en a besoin sur les hosts AppArmor).

---

## ✅ Acquis

### Post-2.1.0 (18-19 juillet 2026)
- [x] **`-c`/`--client`** (19/07) : consommateur pur (DBeaver, Compass, scripts) — atteint les services par nom, **rien de nommé, aucun port réservé sur l'agent**. Exclusif avec `-s` ; ni l'un ni l'autre → la doc du choix. Garde agent ≥ 2.2, câblage launcher→core comme `-s`, cellule e2e ×9 jambes, banc ✅.
- [x] **CI anti-famine** (19/07) : `concurrency` par branche (un push annule le run précédent) + les serves cluster **suivent leur appelant** (`idle-until-caller-done.sh`, poll 60 s — un run annulé n'orpheline plus ses clusters, le TTL n'est que le filet). Leçon au passage : chemin relatif après un `cd` (exit 127 ×6 clusters) → toujours `$root` absolu, et tester le tail depuis le répertoire piégé.
- [x] **Résilience en CI** : cellule `resilience` sur les jambes compose (cluster B — le A partagé ne voit jamais le blip) : takeover tenu sur `res-tko-<leg>`, le service `chaos` (docker.sock, labels compose scopés, répond AVANT de tirer) **redémarre l'agent en pleine session** → keepalive 5 s détecte, boot-gc restaure, le reconnect re-arme et **re-parque** (~10 s de bout en bout au banc), restore final au ttl. Ferme d'un coup : self-heal (**Windows inclus**), boot-gc, re-park au reconnect, re-arm `-s`. Et `k8s-serve.sh` prouve **kubectl port-forward** comme transport à chaque push.
- [x] **NXDOMAIN honnête** (fix fuite DNS) : voir section 🟠 — vérif pré-mint via le verbe agent `resolve`, filtre anti-écho 198.18/15, cellule « dns honesty » ×9 jambes.
- [x] **Drop-loud UDP** : un flux UDP vers un nom minté loggue la cause au lieu du hang silencieux.
- [x] **Dettes** : registry factorisé (`darwin||windows`, tests sur les 2 OS), 4 tests neufs (strip `.plug`, NRPT, channel-reject-sans-reconnexion, ensureVersion), vestiges compose purgés + banc compose local remis au niveau 2.x (`-s` + sock).

### Post-2.0.0 → 2.1.0 (17-18 juillet 2026)
- [x] **Fix macOS re-assert DNS** (17/07, livré en 2.1.0) : le churn mDNSResponder (locationd → DHCP re-publish ~2/min → configd écrase l'override → re-assert + flush + HUP en boucle → échecs getaddrinfo machine-wide) est corrigé — **re-assert silencieux** quand la config effective pointe encore plug, **débounce** max 1 flush/HUP par 30 s (`flushGate`), lignes du daemon.log **timestampées**.
- [x] **Famille Swarm en CI** (18/07) : `swarm-for.yml`, troisième famille sur le même moule — swarm mono-nœud sur le runner, stack dédiée `e2e/swarm.cluster.yml` (configs Swarm pour rabbitmq/mosquitto, mêmes noms/ports). Prouve ce que seul le banc couvrait : l'agent en **service Swarm sur overlay non-attachable** (défaut stack), `-s` provisionne le nom en **service-signpost** sur cet overlay, takeover scale-0 → **retour au replica count d'origine** (tko à 2 replicas → restore-to-N asserté). Banc M5 sur le Swarm existant (stack throwaway) avant push ; CI verte ×3 OS du premier coup. Piège évité : backreference `\1` en ERE non portable (ugrep la refuse) → awk.
- [x] **Famille k8s en CI** (18/07) : toute la chaîne e2e rejouée contre un cluster **kind** (Kubernetes upstream) — `k8s-for.yml` jumeau de `compose-for.yml` (ex-`cluster.yml`), mêmes noms/ports (`e2e/k8s.cluster.yaml`), agent déployé depuis le **manifeste publié** `deploy/plug-k8s.yaml` (RBAC compris, seule l'image changée) → chaque push bénit le fichier que les users déploient. NodePorts mappés sur le runner (`kind-config.yaml`) → contrat `host:2222`/`:18090` inchangé pour les jambes. **Prouvé au runtime** (banc M5 kind, puis CI ×3 OS) : `-s` crée/détruit le Service via le RBAC réel, takeover repointe le selector (reçu-annotation, restore, **ClusterIP identique** à travers park/restore). Deux leçons au passage : probe exec k8s tourne en **root** → race du cookie Erlang rabbitmq (probe tcp à la place) ; le **keep-alive** d'un caller pré-bascule continue d'atteindre l'ancien pod (pods parqués vivants) → prober sans keep-alive + caveat documenté page Kubernetes.
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
